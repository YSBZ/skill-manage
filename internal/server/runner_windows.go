//go:build windows

package server

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
)

// defaultSkillsRunner on Windows routes through `cmd /c` so the npx.cmd batch
// shim resolves, and sets HideWindow + CREATE_NO_WINDOW so the windowless
// (-H=windowsgui) daemon does not flash a console window for the child.
func defaultSkillsRunner() skillsRunner { return windowsRunner{} }

const createNoWindow = 0x08000000 // CREATE_NO_WINDOW

type windowsRunner struct{}

func (windowsRunner) UpdateSkill(ctx context.Context, npxPath, name string) (string, string, error) {
	// npxPath is npx.cmd; reach it through cmd /c. name is allowlist-validated by
	// the caller, so it is a safe discrete arg even via cmd.
	cmd := exec.CommandContext(ctx, "cmd", "/c", npxPath, "skills", "update", name)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}
