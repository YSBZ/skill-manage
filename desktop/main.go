// Command desktop is a Wails PoC: it wraps the existing SkillManage daemon in a
// native desktop window (WKWebView on macOS) instead of a browser tab. The
// window's asset server IS the daemon's own http.Handler — same embedded UI,
// same API, in-process, no localhost port or iframe. This is a proof of concept
// to evaluate the desktop experience and bundle size; it is not yet wired into
// the daemon's single-instance / takeover lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"skillmanage/internal/config"
	"skillmanage/internal/daemon"
	"skillmanage/internal/lock"
	"skillmanage/internal/pathenv"
	"skillmanage/internal/server"
)

// loopbackHost normalizes the request Host/Origin to loopback before handing off
// to the daemon's handler. The Wails webview serves assets from "wails.localhost",
// which the daemon's anti-DNS-rebinding Host check (and the npx endpoints' Origin
// check) reject. These requests originate from the in-process webview, not the
// network, so forcing loopback is safe here.
func loopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1"
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", "http://127.0.0.1")
		}
		// The webview serves the page from wails.localhost, so requests carry a
		// Referer of https://wails.localhost/… . The daemon's origin guard falls
		// back to Referer when Origin is absent (it is for some GETs), and would
		// reject wails.localhost as cross-origin. Normalize it to loopback too.
		if r.Header.Get("Referer") != "" {
			r.Header.Set("Referer", "http://127.0.0.1/")
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	// Same PATH hardening as the daemon, so npx/git work when launched as an app.
	pathenv.Ensure()

	dir, err := config.DefaultCentralDir()
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}

	// GUI launches have no console — log startup + any panic to a file so a
	// crash-on-launch (which looks like the window flickering open/closed) is
	// diagnosable on another machine.
	logf, _ := os.OpenFile(filepath.Join(dir, "desktop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logf != nil {
		log.SetOutput(logf)
		defer logf.Close()
	}
	log.Printf("desktop: starting (pid=%d, exe=%s)", os.Getpid(), mustExe())
	defer func() {
		if r := recover(); r != nil {
			log.Printf("desktop: PANIC: %v\n%s", r, debug.Stack())
			logf.Close()
			os.Exit(1)
		}
	}()

	// Single-instance guard with takeover — same as the CLI daemon, so launching
	// the app while a terminal instance runs cleanly replaces it (and two app
	// launches don't double-run the scheduler / race the manifest).
	lockPath := config.LockfilePath(dir)
	lk, err := lock.Acquire(lockPath)
	if errors.Is(err, lock.ErrLocked) {
		if lk, err = daemon.TakeOver(dir, lockPath); err != nil {
			fmt.Fprintln(os.Stderr, "desktop: 已有一个实例在运行且无法接管")
			os.Exit(1)
		}
	} else if err != nil {
		panic(err)
	}
	defer lk.Release()
	daemon.WritePid(dir)
	defer daemon.RemovePid(dir)

	log.Printf("desktop: lock acquired, starting server")
	srv, err := server.New(dir)
	if err != nil {
		panic(err)
	}
	defer srv.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	srv.SetBaseContext(ctx)
	srv.ReconcileOnly()
	go srv.RunScheduler(ctx)

	log.Printf("desktop: opening window")
	err = wails.Run(&options.App{
		Title:  "SkillManage",
		Width:  1180,
		Height: 820,
		// The window loads the daemon's UI through its own handler — no browser,
		// no port. Relative /api/* fetches resolve back to this same handler.
		AssetServer: &assetserver.Options{Handler: loopbackHost(srv.Handler())},
		// Standard title bar: a normal draggable bar with the traffic lights in it,
		// so they don't overlap the app's own header. (HiddenInset looked nicer but
		// left no drag region and collided with the logo.)
		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
		},
	})
	// Window closed / app quit → cancel the scheduler and drain in-flight syncs
	// before the deferred srv.Close()/lk.Release() tear things down.
	stop()
	srv.WaitForSyncs()
	if err != nil {
		log.Printf("desktop: wails.Run error: %v", err)
		fmt.Fprintln(os.Stderr, "desktop:", err)
	}
	log.Printf("desktop: clean exit")
}

func mustExe() string { e, _ := os.Executable(); return e }
