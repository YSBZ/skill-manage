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
