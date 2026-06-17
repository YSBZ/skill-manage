// Package daemon holds the single-instance lifecycle primitives shared by the
// CLI entry point (main.go) and the desktop app (desktop/main.go): recording a
// PID, and taking over a running instance so a relaunch "just restarts". Keeping
// these in one place means both front ends behave identically — launch the app
// while a terminal daemon runs and it cleanly takes over port 7799.
package daemon

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"skillmanage/internal/config"
	"skillmanage/internal/lock"
)

// WritePid records this process's id so a later launch can stop us and take over.
func WritePid(dir string) {
	_ = os.WriteFile(config.PidPath(dir), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemovePid deletes the PID file (call on shutdown).
func RemovePid(dir string) { _ = os.Remove(config.PidPath(dir)) }

// ReadPid returns the recorded PID of the running instance, or 0 if absent/invalid.
func ReadPid(dir string) int {
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

// TakeOver stops the running instance (recorded PID) and acquires the lock once
// its flock releases. SIGTERM first (clean shutdown drains syncs + releases the
// lock); escalate to Kill if it does not exit promptly. Returns ErrLocked if the
// predecessor cannot be identified or refuses to die.
func TakeOver(dir, lockPath string) (*lock.Lock, error) {
	pid := ReadPid(dir)
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

// ReadAddressURL returns the UI URL the running instance recorded, or "".
func ReadAddressURL(dir string) string {
	b, err := os.ReadFile(config.AddressPath(dir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
