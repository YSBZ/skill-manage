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
	// -g --yes: global scope + skip the interactive prompt (daemon has no tty).
	cmd := exec.CommandContext(ctx, "cmd", "/c", npxPath, "skills", "update", name, "-g", "--yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// UpdateAll runs `npx skills update -g --yes` (no skill arg → updates everything).
func (windowsRunner) UpdateAll(ctx context.Context, npxPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", npxPath, "skills", "update", "-g", "--yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// UpdatePlugin delegates to `claude plugin update <plugin> -s <scope>` via cmd /c
// (claude is a .cmd shim on Windows). plugin/scope validated by the caller.
func (windowsRunner) UpdatePlugin(ctx context.Context, cliPath, plugin, scope string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", cliPath, "plugin", "update", plugin, "-s", scope)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// ListPlugins runs `claude plugin list --json` (local, no marketplace) via cmd /c.
func (windowsRunner) ListPlugins(ctx context.Context, cliPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", cliPath, "plugin", "list", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// ListMarketplaces runs `claude plugin marketplace list --json` via cmd /c.
func (windowsRunner) ListMarketplaces(ctx context.Context, cliPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", cliPath, "plugin", "marketplace", "list", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsFind runs `npx skills find <query>` via cmd /c. query is caller-guarded
// (non-empty, no leading "-").
func (windowsRunner) SkillsFind(ctx context.Context, npxPath, query string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", npxPath, "skills", "find", query)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsAdd runs `npx skills add <pkg> -g -y -a universal` via cmd /c — global,
// non-interactive, canonical-only (no harness symlinks; see runner_unix.go for the
// flag rationale). pkg is caller-validated (owner/repo@skill form).
func (windowsRunner) SkillsAdd(ctx context.Context, npxPath, pkg string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", npxPath, "skills", "add", pkg, "-g", "-y", "-a", "universal")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsRemove runs `npx skills remove <name> -g -y` via cmd /c — no -a, which
// auto-targets all agents + the global canonical (verified). name caller-validated.
func (windowsRunner) SkillsRemove(ctx context.Context, npxPath, name string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "cmd", "/c", npxPath, "skills", "remove", name, "-g", "-y")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}
