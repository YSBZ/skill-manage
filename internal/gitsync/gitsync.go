// Package gitsync clones and refreshes tracked skill repos as strict read-only
// mirrors, using daemon-safe, non-interactive, hook-disabled git (KTD6).
package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrDirty is returned (as Result.Err) when an existing mirror has local
// modifications and Options.Force is false. The caller decides whether to warn
// the user (R26) or force-overwrite to restore the read-only-mirror invariant.
var ErrDirty = errors.New("working tree has local modifications")

// Options controls a sync.
type Options struct {
	// Branch to track. Empty means track the remote's default branch.
	Branch string
	// Force discards local drift (`reset --hard` + `clean -fd`) even when the
	// working tree is dirty. When false, a dirty tree is surfaced via ErrDirty
	// and left untouched (R26).
	Force bool
}

// Action describes what a sync did.
type Action string

const (
	ActionCloned    Action = "cloned"
	ActionSynced    Action = "synced"
	ActionDirtySkip Action = "dirty-skip"
	ActionFailed    Action = "failed"
)

// Result is the per-repo outcome (R7).
type Result struct {
	URL    string
	Dir    string
	OK     bool
	Action Action
	Dirty  bool
	Err    error
	Stderr string
}

// Syncer runs git against a resolved git binary with an empty hooks directory
// so repo-supplied hooks never execute during sync (R25).
type Syncer struct {
	git      string
	hooksDir string
}

// NewSyncer resolves the system git and creates the empty hooks directory.
func NewSyncer() (*Syncer, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git not found on PATH: %w", err)
	}
	hooksDir, err := os.MkdirTemp("", "skillmanage-nohooks-")
	if err != nil {
		return nil, fmt.Errorf("create empty hooks dir: %w", err)
	}
	return &Syncer{git: gitPath, hooksDir: hooksDir}, nil
}

// Close removes the temporary hooks directory.
func (s *Syncer) Close() error {
	if s.hooksDir == "" {
		return nil
	}
	return os.RemoveAll(s.hooksDir)
}

// Sync validates the URL (R24) then mirrors the repo at dir.
func (s *Syncer) Sync(ctx context.Context, dir, url string, opts Options) Result {
	if err := ValidateRepoURL(url); err != nil {
		return Result{URL: url, Dir: dir, Action: ActionFailed, Err: err}
	}
	return s.sync(ctx, dir, url, opts)
}

// sync is the validation-free core, so tests can drive it with a local fixture
// remote (which the public Sync's allowlist would reject).
func (s *Syncer) sync(ctx context.Context, dir, remote string, opts Options) Result {
	res := Result{URL: remote, Dir: dir}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		// Fresh clone.
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			res.Action, res.Err = ActionFailed, fmt.Errorf("create parent dir: %w", err)
			return res
		}
		args := []string{"clone"}
		if opts.Branch != "" {
			args = append(args, "--branch", opts.Branch)
		}
		args = append(args, "--", remote, dir)
		if _, stderr, err := s.run(ctx, "", args...); err != nil {
			res.Action, res.Err, res.Stderr = ActionFailed, err, stderr
			return res
		}
		res.OK, res.Action = true, ActionCloned
		return res
	}

	// Existing mirror: fetch.
	if _, stderr, err := s.run(ctx, dir, "fetch", "--prune", "origin"); err != nil {
		res.Action, res.Err, res.Stderr = ActionFailed, err, stderr
		return res
	}

	// Dirty check (R26): never silently discard local edits unless forced.
	if out, _, err := s.run(ctx, dir, "status", "--porcelain"); err == nil && strings.TrimSpace(out) != "" {
		res.Dirty = true
		if !opts.Force {
			res.Action, res.Err = ActionDirtySkip, ErrDirty
			return res
		}
	}

	ref := "origin/HEAD"
	if opts.Branch != "" {
		ref = "origin/" + opts.Branch
	}
	if _, stderr, err := s.run(ctx, dir, "reset", "--hard", ref); err != nil {
		res.Action, res.Err, res.Stderr = ActionFailed, err, stderr
		return res
	}
	// clean -fd: reset --hard leaves untracked files; without this an
	// upstream-deleted skill dir could survive and be re-scanned as live.
	if _, stderr, err := s.run(ctx, dir, "clean", "-fd"); err != nil {
		res.Action, res.Err, res.Stderr = ActionFailed, err, stderr
		return res
	}
	res.OK, res.Action = true, ActionSynced
	return res
}

// run executes git with daemon-safe flags and env, returning (stdout, stderr, err).
func (s *Syncer) run(ctx context.Context, dir string, args ...string) (string, string, error) {
	full := append([]string{"-c", "core.hooksPath=" + s.hooksDir}, args...)
	cmd := exec.CommandContext(ctx, s.git, full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",                // no interactive credential prompt
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes", // no interactive ssh prompt
		"GCM_INTERACTIVE=never",                // Git Credential Manager: no popups
		"GIT_CONFIG_NOSYSTEM=1",                // ignore /etc system config (keeps global helper)
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), stderr.String(), nil
}
