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
	cmd := exec.CommandContext(ctx, npxPath, "skills", "update", name)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}
