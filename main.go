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
	"strconv"
	"strings"
	"syscall"
	"time"

	"skillmanage/internal/browser"
	"skillmanage/internal/config"
	"skillmanage/internal/lock"
	"skillmanage/internal/pathenv"
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
	noOpen := flag.Bool("no-open", false, "do not open the browser on launch")
	flag.Parse()

	dir := *centralDir
	if dir == "" {
		d, err := config.DefaultCentralDir()
		if err != nil {
			fatal("", err)
		}
		dir = d
	}
	if err := run(dir, !*noOpen); err != nil {
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

// writePid records this process's id so a later launch can stop us and take over.
func writePid(dir string) {
	_ = os.WriteFile(config.PidPath(dir), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// readPid returns the recorded PID of the running instance, or 0 if absent/invalid.
func readPid(dir string) int {
	b, err := os.ReadFile(config.PidPath(dir))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// takeOver stops the running instance (recorded PID) and acquires the lock once
// its flock releases. SIGTERM first (clean shutdown drains syncs + releases the
// lock); escalate to Kill if it does not exit promptly. Returns ErrLocked if the
// predecessor cannot be identified or refuses to die.
func takeOver(dir, lockPath string) (*lock.Lock, error) {
	pid := readPid(dir)
	if pid <= 0 || pid == os.Getpid() {
		return nil, lock.ErrLocked // no one to stop / stale-without-pid → defer
	}
	signalPid(pid, false) // graceful
	deadline := time.Now().Add(8 * time.Second)
	escalated := false
	for time.Now().Before(deadline) {
		lk, err := lock.Acquire(lockPath)
		if err == nil {
			return lk, nil
		}
		if !errors.Is(err, lock.ErrLocked) {
			return nil, err
		}
		if !escalated && time.Now().Add(4*time.Second).After(deadline) {
			signalPid(pid, true) // still alive past the grace window → force-kill
			escalated = true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil, lock.ErrLocked
}

// signalPid sends a stop signal to pid. force=false asks for a graceful shutdown
// (SIGTERM on unix); force=true kills outright. On Windows SIGTERM is unsupported,
// so both paths fall back to Kill.
func signalPid(pid int, force bool) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if force {
		_ = p.Kill()
		return
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		_ = p.Kill()
	}
}

// readAddressURL returns the UI URL the running instance recorded, or "".
func readAddressURL(dir string) string {
	b, err := os.ReadFile(config.AddressPath(dir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func run(dir string, openBrowser bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Harden PATH before anything resolves git/npx: a GUI/launchd-started daemon
	// inherits only a minimal PATH on macOS, which would hide Homebrew/nvm tools.
	pathenv.Ensure()

	// Single-instance guard before any sync/reconcile (KTD8).
	lockPath := config.LockfilePath(dir)
	lk, err := lock.Acquire(lockPath)
	if errors.Is(err, lock.ErrLocked) {
		// A previous instance is running. On launch we TAKE OVER: stop the
		// predecessor and become the live instance, so re-launching the package
		// "just restarts" instead of deferring to a stale window. If we cannot
		// identify/stop it, fall back to pointing at the existing instance.
		if lk, err = takeOver(dir, lockPath); err != nil {
			if url := readAddressURL(dir); url != "" {
				fmt.Printf("skillmanage: 已在运行且无法接管，控制台 %s\n", url)
				if openBrowser {
					_ = browser.Open(url)
				}
			} else {
				fmt.Println("skillmanage: 已有一个实例在运行，且无法接管。")
			}
			return nil
		}
		fmt.Println("skillmanage: 已关闭上一个实例并接管。")
	} else if err != nil {
		return err
	}
	defer lk.Release()

	// Record our PID so the next launch can find and stop us to take over.
	writePid(dir)
	defer os.Remove(config.PidPath(dir))

	srv, err := server.New(dir)
	if err != nil {
		return err
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Request-detached syncs (update-now) parent off this context so they
	// survive a closed browser tab but still cancel on daemon shutdown.
	srv.SetBaseContext(ctx)

	// On launch we only RECONCILE — materialize the links the current config asks
	// for, without touching git — so existing mirrors map correctly. We do NOT pull
	// on startup; pulling happens on the daily schedule below or when the user
	// clicks 全量更新.
	srv.ReconcileOnly()

	// Daily scheduler: once a day at the configured wall-clock time (default
	// 09:40 local) do a full update — pull git sources and delegate skills.sh
	// sources to `npx skills update`. Cancels with ctx on shutdown.
	go srv.RunScheduler(ctx)

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
