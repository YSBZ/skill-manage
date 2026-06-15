// Command skillmanage is a single-binary daemon that tracks git skill repos,
// keeps them fresh on a daily schedule, and links selected skills into Claude
// Code's skill directories — driven by an embedded browser UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"skillmanage/internal/autostart"
	"skillmanage/internal/browser"
	"skillmanage/internal/config"
	"skillmanage/internal/lock"
	"skillmanage/internal/scheduler"
	"skillmanage/internal/server"
)

const defaultPort = 7799

func main() {
	// Credential helper mode: git invokes this same binary as GIT_ASKPASS during
	// a fetch that needs HTTPS credentials. Handle it and exit before the normal
	// daemon flow. (gitsync sets SKILLMANAGE_ASKPASS + SKILLMANAGE_CENTRAL.)
	if os.Getenv("SKILLMANAGE_ASKPASS") != "" {
		askpass()
		return
	}

	centralDir := flag.String("central", "", "central folder (default ~/.skillmanage)")
	noAutostart := flag.Bool("no-autostart", false, "do not register login autostart on first run")
	noOpen := flag.Bool("no-open", false, "do not open the browser on launch (autostart-launched instances pass this)")
	flag.Parse()

	dir := *centralDir
	if dir == "" {
		d, err := config.DefaultCentralDir()
		if err != nil {
			fatal("", err)
		}
		dir = d
	}
	if err := run(dir, !*noAutostart, !*noOpen); err != nil {
		fatal(dir, err)
	}
}

// askpass answers git's credential prompt from the stored per-host credentials.
// git calls it with one arg, e.g. "Username for 'https://host': " or "Password
// for 'https://user@host': ". We print the username or PAT for that host; an
// unknown host prints nothing, so git fails exactly as it did before (no creds).
func askpass() {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	dir := os.Getenv("SKILLMANAGE_CENTRAL")
	if dir == "" {
		return
	}
	creds, err := config.LoadCredentials(dir)
	if err != nil {
		return
	}
	cred, ok := creds.Hosts[hostFromPrompt(prompt)]
	if !ok {
		return
	}
	switch {
	case strings.HasPrefix(strings.ToLower(prompt), "username"):
		fmt.Println(cred.Username)
	case strings.HasPrefix(strings.ToLower(prompt), "password"):
		fmt.Println(cred.Token)
	}
}

// hostFromPrompt extracts the host from a git askpass prompt by parsing the URL
// between single quotes (userinfo, if any, is dropped).
func hostFromPrompt(prompt string) string {
	i := strings.IndexByte(prompt, '\'')
	if i < 0 {
		return ""
	}
	rest := prompt[i+1:]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return ""
	}
	u, err := url.Parse(rest[:j])
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// fatal reports a startup failure to stderr AND a log file under the central
// dir. The log file is the only diagnostic on Windows, where the binary is
// built windowless (no console to read stderr from) — without it a failed
// launch is just a window that flashes and vanishes.
func fatal(dir string, err error) {
	msg := "skillmanage: " + err.Error()
	fmt.Fprintln(os.Stderr, msg)
	if dir != "" {
		if f, e := os.OpenFile(filepath.Join(dir, "skillmanage.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); e == nil {
			fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), msg)
			_ = f.Close()
		}
	}
	os.Exit(1)
}

// readAddressURL returns the UI URL the running instance recorded, or "".
func readAddressURL(dir string) string {
	b, err := os.ReadFile(config.AddressPath(dir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func run(dir string, registerAutostart, openBrowser bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Single-instance guard before any sync/reconcile (KTD8).
	lk, err := lock.Acquire(config.LockfilePath(dir))
	if errors.Is(err, lock.ErrLocked) {
		// Already running. A double-clicked window would otherwise just print an
		// error and vanish ("闪一下就关了"); instead point the user at the live
		// instance and exit cleanly.
		if openBrowser {
			if url := readAddressURL(dir); url != "" {
				_ = browser.Open(url)
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer lk.Release()

	srv, err := server.New(dir)
	if err != nil {
		return err
	}
	defer srv.Close()

	// Wire autostart and (re-)register on launch (R19), best-effort. Always
	// re-register when enabled so the recorded command refreshes — picking up a
	// moved binary and the --no-open arg that keeps login-launched instances
	// from popping a browser every login.
	if exe, err := os.Executable(); err == nil {
		if mgr, err := autostart.New(exe); err == nil {
			srv.SetAutostart(mgr)
			if registerAutostart {
				_ = mgr.Register()
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Request-detached syncs (update-now) parent off this context so they
	// survive a closed browser tab but still cancel on daemon shutdown.
	srv.SetBaseContext(ctx)

	// Daily scheduler + an initial sync on launch.
	sched, err := scheduler.New(srv.Schedule(), func(c context.Context) { srv.SyncAll(c, false) })
	if err != nil {
		return err
	}
	// Pass the persisted last-sync time so the scheduler's missed-run check can
	// actually fire after a sleep/downtime gap (not time.Now(), which disables it).
	go sched.Run(ctx, srv.LastSync())
	go srv.SyncAll(ctx, false)

	ln, err := srv.Bind(defaultPort)
	if err != nil {
		return err
	}
	if err := srv.WriteAddress(ln.Addr().String()); err != nil {
		fmt.Fprintln(os.Stderr, "skillmanage: warning: could not write address file:", err)
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	fmt.Printf("skillmanage: UI at %s\n", url)
	// Open the UI for an interactive launch (double-click / manual run). Skipped
	// for autostart-launched instances, which pass --no-open.
	if openBrowser {
		_ = browser.Open(url)
	}

	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	serveErr := httpSrv.Serve(ln)
	// Serve has returned → the daemon is shutting down and ctx is cancelled.
	// Drain in-flight syncs (the launch sync, any scheduled run, a detached
	// update-now) before the deferred lock release and srv.Close() remove the
	// git hooks dir out from under a still-running git subprocess.
	srv.WaitForSyncs()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
