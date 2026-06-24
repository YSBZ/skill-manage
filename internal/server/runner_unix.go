//go:build !windows

package server

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
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

// SkillsAddURL runs `npx skills add <repoURL> --skill <name> -g -y -a universal`
// — the skillsmp.com install form (repo URL + skill name). Same canonical-only,
// non-interactive flags as SkillsAdd; repoURL/skill are allowlist-validated by the
// caller, discrete argv (no shell).
func (unixRunner) SkillsAddURL(ctx context.Context, npxPath, repoURL, skill string) (string, string, error) {
	cmd := exec.CommandContext(ctx, npxPath, "skills", "add", repoURL, "--skill", skill, "-g", "-y", "-a", "universal")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsMpFind fetches a skillsmp.com search URL with curl (Cloudflare 403s Go's
// TLS fingerprint; curl's passes). -fsS: fail on HTTP>=400, silent, but show errors.
func (unixRunner) SkillsMpFind(ctx context.Context, url, apiKey string) (string, string, error) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		return "", "需要 curl 才能搜索 skillsmp", err
	}
	args := []string{"-fsS", "--max-time", "20", "-H", "Accept: application/json"}
	var stdin *strings.Reader
	if apiKey != "" {
		// Pass the API key via a curl config read from stdin, NOT argv — keeps the
		// secret out of `ps`/process listings. Key charset is alnum/_/-, no quotes.
		args = append(args, "-K", "-")
		stdin = strings.NewReader("header = \"Authorization: Bearer " + apiKey + "\"\n")
	}
	args = append(args, "-w", skillsMpRateWriteout, url) // capture daily quota headers after the body
	cmd := exec.CommandContext(ctx, curl, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
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

// SkillsAdd runs `npx skills add <pkg> -g -y -a universal`. The flags, all
// verified against the real CLI:
//
//	-g           global (user-level) install — never project-level (KTD1: would scatter installs)
//	-y           skip every interactive prompt (the daemon has no tty)
//	-a universal install ONLY to the canonical ~/.agents/skills, with NO harness
//	             symlinks (KTD4: install ≠ enable). Without -a, `-y` defaults to
//	             symlinking into ALL ~50 detected agent dirs (incl. ~/.claude,
//	             ~/.codex) — which would auto-enable everywhere and break the
//	             two-step model. `universal` is the agent id for canonical-only.
//
// pkg is allowlist-validated by the caller (owner/repo@skill form).
func (unixRunner) SkillsAdd(ctx context.Context, npxPath, pkg string) (string, string, error) {
	cmd := exec.CommandContext(ctx, npxPath, "skills", "add", pkg, "-g", "-y", "-a", "universal")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SkillsRemove runs `npx skills remove <name> -g -y`. Verified against the real
// CLI: with no -a flag, remove auto-targets ALL agents + the global canonical, so
// it drops ~/.agents/skills/<name> AND every agent symlink skills.sh made. (`-a '*'`
// is rejected as "Invalid agents".) SkillManage's own manifest-owned links are torn
// down separately by the caller (reconcile) before this runs. name caller-validated.
func (unixRunner) SkillsRemove(ctx context.Context, npxPath, name string) (string, string, error) {
	cmd := exec.CommandContext(ctx, npxPath, "skills", "remove", name, "-g", "-y")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}
