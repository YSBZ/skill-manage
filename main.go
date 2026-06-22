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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"skillmanage/internal/askpass"
	"skillmanage/internal/browser"
	"skillmanage/internal/config"
	"skillmanage/internal/daemon"
	"skillmanage/internal/lock"
	"skillmanage/internal/pathenv"
	"skillmanage/internal/server"
)

const defaultPort = 7799

func main() {
	// Credential helper mode: git invokes this same binary as GIT_ASKPASS during
	// a fetch/push that needs HTTPS credentials. Handle it and exit before the
	// normal daemon flow. (gitsync sets SKILLMANAGE_ASKPASS + SKILLMANAGE_CENTRAL.)
	if askpass.Active() {
		askpass.Run()
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
		if lk, err = daemon.TakeOver(dir, lockPath); err != nil {
			if url := daemon.ReadAddressURL(dir); url != "" {
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
	daemon.WritePid(dir)
	defer daemon.RemovePid(dir)

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
