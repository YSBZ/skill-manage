//go:build windows

package gitsync

import (
	"os/exec"
	"syscall"
)

// hideConsole stops a child process (git, and the askpass helper git spawns) from
// flashing a console window when the host app is windowless (-H=windowsgui). The
// desktop app has no console, so each git invocation would otherwise pop a brief
// cmd window. HideWindow + CREATE_NO_WINDOW suppress it; children inherit it.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
