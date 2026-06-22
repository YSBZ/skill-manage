//go:build !windows

package gitsync

import "os/exec"

// hideConsole is a no-op off Windows: there is no stray console window to hide.
func hideConsole(cmd *exec.Cmd) {}
