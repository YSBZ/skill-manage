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
	"syscall"
	"time"

	"skillmanage/internal/autostart"
	"skillmanage/internal/config"
	"skillmanage/internal/lock"
	"skillmanage/internal/scheduler"
	"skillmanage/internal/server"
)

const defaultPort = 7799

func main() {
	centralDir := flag.String("central", "", "central folder (default ~/.skillmanage)")
	noAutostart := flag.Bool("no-autostart", false, "do not register login autostart on first run")
	flag.Parse()

	if err := run(*centralDir, !*noAutostart); err != nil {
		fmt.Fprintln(os.Stderr, "skillmanage:", err)
		os.Exit(1)
	}
}

func run(centralDir string, registerAutostart bool) error {
	dir := centralDir
	if dir == "" {
		d, err := config.DefaultCentralDir()
		if err != nil {
			return err
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Single-instance guard before any sync/reconcile (KTD8).
	lk, err := lock.Acquire(config.LockfilePath(dir))
	if errors.Is(err, lock.ErrLocked) {
		return fmt.Errorf("already running (lock held at %s)", config.LockfilePath(dir))
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

	// Wire autostart and register on launch (R19), best-effort.
	if exe, err := os.Executable(); err == nil {
		if mgr, err := autostart.New(exe); err == nil {
			srv.SetAutostart(mgr)
			if registerAutostart && !mgr.IsRegistered() {
				_ = mgr.Register()
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Daily scheduler + an initial sync on launch.
	sched, err := scheduler.New(srv.Schedule(), func(c context.Context) { srv.SyncAll(c, false) })
	if err != nil {
		return err
	}
	go sched.Run(ctx, time.Now())
	go srv.SyncAll(ctx, false)

	ln, err := srv.Bind(defaultPort)
	if err != nil {
		return err
	}
	if err := srv.WriteAddress(ln.Addr().String()); err != nil {
		fmt.Fprintln(os.Stderr, "skillmanage: warning: could not write address file:", err)
	}
	fmt.Printf("skillmanage: UI at http://%s/\n", ln.Addr().String())

	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
