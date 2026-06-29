//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// alivePlatform probes pid with signal 0 (no-op delivery that still validates the
// target exists and is signalable by us).
func alivePlatform(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// signalPidPlatform asks pid to stop: SIGTERM for a graceful drain, escalating to
// Kill when forced or when SIGTERM is rejected.
func signalPidPlatform(pid int, force bool) {
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

// killOtherInstancesPlatform is a no-op on Unix: the pid+signal takeover model is
// reliable here, so the by-name process sweep (a Windows-only safety net) is not
// needed. Returning 0 keeps TakeOver's escalation path inert off Windows.
func killOtherInstancesPlatform(self int) int { return 0 }
