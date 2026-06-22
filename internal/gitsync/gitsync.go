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
	"time"
)

// defaultCmdTimeout bounds a single git invocation. update-now detaches the
// request context (so closing the tab does not cancel a sync), which means a
// git process blocked on an unreachable host would otherwise hang forever and
// pin a sync slot. Skill repos are small, so a few minutes is generous.
const defaultCmdTimeout = 5 * time.Minute

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
	// Drift carries the per-skill local-change detail when Dirty is set, so the
	// UI can show what was preserved (新增/修改/已提交未推送) and offer 快捷上传.
	// Empty on a clean or freshly-cloned repo.
	Drift Drift
}

// Syncer runs git against a resolved git binary with an empty hooks directory
// so repo-supplied hooks never execute during sync (R25).
type Syncer struct {
	git        string
	hooksDir   string
	cmdTimeout time.Duration // per-command wall-clock bound; <=0 disables
	askpassExe string        // this binary, used as GIT_ASKPASS for HTTPS creds
	centralDir string        // where the credential file lives (passed to askpass)
}

// SetAskpass wires the daemon's own binary as git's credential helper so a fetch
// needing HTTPS credentials reads them from the stored per-host credentials
// instead of failing. No-op fields leave auth unchanged (SSH / system helper).
func (s *Syncer) SetAskpass(exe, centralDir string) {
	s.askpassExe = exe
	s.centralDir = centralDir
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
	return &Syncer{git: gitPath, hooksDir: hooksDir, cmdTimeout: defaultCmdTimeout}, nil
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

	ref := "origin/HEAD"
	if opts.Branch != "" {
		ref = "origin/" + opts.Branch
	}

	// Dirty check (R26 + commit-aware drift): never silently discard local edits
	// unless forced. "Dirty" now means a non-empty working tree OR local commits
	// not on the upstream ref (committed-unpushed) — the latter is invisible to
	// `status --porcelain` and is exactly the state a push-failed contribution
	// sits in, so without it the next reset --hard would destroy it (the P0 the
	// plan review caught). When dirty, attach the per-skill detail for the UI.
	if d := s.driftAt(ctx, dir, ref); d.Dirty {
		res.Dirty = true
		res.Drift = d
		if !opts.Force {
			res.Action, res.Err = ActionDirtySkip, ErrDirty
			return res
		}
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

// CheckUpdate reports whether origin's branch (or HEAD when branch is empty) is
// at a different commit than what we last fetched — i.e. an update is available
// to pull. It uses `ls-remote` (no object download) so it is cheap, but it still
// contacts the remote and needs the same auth as Sync; a failure (auth/network)
// is returned so the caller can surface it.
//
// It compares the upstream tip against the local remote-tracking ref
// (refs/remotes/origin/<branch>), NOT local HEAD: once contribution lets HEAD
// hold unpushed local commits, HEAD != upstream no longer means "upstream
// moved". The tracking ref still reflects the last fetched upstream state, so
// comparing against it stays correct in the presence of local drift.
func (s *Syncer) CheckUpdate(ctx context.Context, dir, branch string) (bool, error) {
	trackRef := "refs/remotes/origin/HEAD"
	if b := strings.TrimSpace(branch); b != "" {
		trackRef = "refs/remotes/origin/" + b
	}
	localOut, _, err := s.run(ctx, dir, "rev-parse", trackRef)
	if err != nil {
		return false, err
	}
	local := strings.TrimSpace(localOut)
	ref := "HEAD"
	if b := strings.TrimSpace(branch); b != "" {
		ref = "refs/heads/" + b
	}
	remoteOut, stderr, err := s.run(ctx, dir, "ls-remote", "origin", ref)
	if err != nil {
		if e := strings.TrimSpace(stderr); e != "" {
			return false, fmt.Errorf("%s", e)
		}
		return false, err
	}
	line := strings.TrimSpace(remoteOut)
	if line == "" {
		return false, nil // ref absent upstream → treat as no update
	}
	remote := strings.Fields(line)[0]
	return remote != "" && remote != local, nil
}

// run executes git with daemon-safe flags and env, returning (stdout, stderr, err).
func (s *Syncer) run(ctx context.Context, dir string, args ...string) (string, string, error) {
	return s.runEnv(ctx, dir, nil, args...)
}

// runEnv is run with caller-supplied extra environment appended last (so it
// wins on duplicate keys). The push path uses it to set LC_ALL=C, forcing
// git's stderr into stable English for error-code matching (KTD5 honest
// judgment) without disturbing the read-only sync path.
func (s *Syncer) runEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, string, error) {
	// Bound each invocation so a process blocked on an unreachable remote
	// cannot hang indefinitely (a detached update-now context has no deadline
	// of its own). A zero timeout (test-constructed Syncers) disables this.
	if s.cmdTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cmdTimeout)
		defer cancel()
	}
	full := append([]string{"-c", "core.hooksPath=" + s.hooksDir}, args...)
	cmd := exec.CommandContext(ctx, s.git, full...)
	hideConsole(cmd) // Windows: suppress the flashing console window (no-op elsewhere)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",                // no interactive credential prompt
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes", // no interactive ssh prompt
		"GCM_INTERACTIVE=never",                // Git Credential Manager: no popups
		"GIT_CONFIG_NOSYSTEM=1",                // ignore /etc system config (keeps global helper)
	)
	// Feed stored HTTPS credentials via our own binary as GIT_ASKPASS. Unset
	// fields (SSH-only setups) leave git auth untouched.
	if s.askpassExe != "" && s.centralDir != "" {
		cmd.Env = append(cmd.Env,
			"GIT_ASKPASS="+s.askpassExe,
			"SKILLMANAGE_ASKPASS=1",
			"SKILLMANAGE_CENTRAL="+s.centralDir,
		)
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Env, extraEnv...) // appended last → wins on dup keys
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), stderr.String(), nil
}
