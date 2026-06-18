//go:build !windows

package server

import (
	"bytes"
	"context"
	"os/exec"
)

// defaultSkillsRunner runs npx directly on Unix — npx is a real executable on
// PATH and there is no console window to suppress.
func defaultSkillsRunner() skillsRunner { return unixRunner{} }

type unixRunner struct{}

func (unixRunner) UpdateSkill(ctx context.Context, npxPath, name string) (string, string, error) {
	// Discrete argv (never a shell): name has already been allowlist-validated by
	// the caller, and passing it as a separate arg gives no shell metachar surface.
	// -g --yes: target the global ~/.agents/skills (our canonical) and skip the
	// interactive scope prompt — the daemon has no tty, so without --yes the
	// command would block on the prompt until timeout.
	cmd := exec.CommandContext(ctx, npxPath, "skills", "update", name, "-g", "--yes")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// UpdateAll runs `npx skills update -g --yes` (no skill argument → every global
// skill skills.sh manages). --yes is mandatory: the daemon is non-interactive.
func (unixRunner) UpdateAll(ctx context.Context, npxPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, npxPath, "skills", "update", "-g", "--yes")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// UpdatePlugin delegates to the harness CLI: `claude plugin update <plugin> -s
// <scope>`. plugin/scope are validated by the caller; discrete argv (no shell).
func (unixRunner) UpdatePlugin(ctx context.Context, cliPath, plugin, scope string) (string, string, error) {
	cmd := exec.CommandContext(ctx, cliPath, "plugin", "update", plugin, "-s", scope)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// ListPlugins runs `claude plugin list --json` (local, no marketplace fetch) so
// the server can resolve each installed plugin's full id + scope for delegated
// update. The marketplace (--available) is not used: it does not expose a
// comparable version for these plugins, so update detection isn't possible.
func (unixRunner) ListPlugins(ctx context.Context, cliPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, cliPath, "plugin", "list", "--json")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// ListMarketplaces runs `claude plugin marketplace list --json` so the server can
// tell remote marketplaces (source github/git → updatable) from local directory
// marketplaces (source directory → self-made, not tracked).
func (unixRunner) ListMarketplaces(ctx context.Context, cliPath string) (string, string, error) {
	cmd := exec.CommandContext(ctx, cliPath, "plugin", "marketplace", "list", "--json")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsFind runs `npx skills find <query>` to search the skills.sh registry.
// query is allowlist-guarded by the caller (rejected if empty or leading "-",
// which the CLI would parse as a flag); discrete argv, never a shell.
func (unixRunner) SkillsFind(ctx context.Context, npxPath, query string) (string, string, error) {
	cmd := exec.CommandContext(ctx, npxPath, "skills", "find", query)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsAdd runs `npx skills add <pkg> -g -y`. -g forces global (user-level)
// install into skills.sh's canonical ~/.agents/skills (KTD1: never project-level,
// which would scatter installs); -y skips every interactive prompt (the daemon
// has no tty). pkg is allowlist-validated by the caller (owner/repo@skill form).
func (unixRunner) SkillsAdd(ctx context.Context, npxPath, pkg string) (string, string, error) {
	cmd := exec.CommandContext(ctx, npxPath, "skills", "add", pkg, "-g", "-y")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}
