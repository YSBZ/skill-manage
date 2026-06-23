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

// osJunkPatterns are OS/editor cruft that must never reach a shared skill repo.
// macOS Finder drops a .DS_Store into every directory it browses, and the mirror
// working tree gets browsed (by Finder and by this app), so without ignoring them
// they surface as "added" drift and would be committed + pushed upstream.
var osJunkPatterns = []string{
	// macOS Finder / Spotlight / Time Machine cruft.
	".DS_Store", "._*", ".Spotlight-V100", ".Trashes", ".AppleDouble",
	".fseventsd", ".TemporaryItems", ".DocumentRevisions-V100", ".apdisk",
	// Windows shell cruft.
	"Thumbs.db", "ehthumbs.db", "desktop.ini", "$RECYCLE.BIN",
	// Linux desktop / NFS cruft.
	".directory", ".nfs*", ".Trash-*",
	// Editor swap / backup / lock files (never legitimate skill content).
	"*.swp", "*.swo", "*.swn", "*~", ".#*", "#*#",
	// IDE workspace dirs (info/exclude only hides them when untracked — if a repo
	// genuinely tracks them upstream, tracking wins and they're unaffected).
	".idea/", ".vscode/",
	// Dependency / build artifacts that should never live in a shared skill repo.
	"node_modules/", "__pycache__/", "*.pyc", ".venv/", "venv/", "*.log",
}

// ensureExcludes makes a mirror's git ignore OS junk by appending the patterns to
// .git/info/exclude — a LOCAL, per-repo ignore that never touches (or pushes) the
// upstream .gitignore. With these ignored, `git status` won't report them as
// drift and `git add` won't stage them, so a single write covers both the
// dirty-check and the contribute path. Idempotent (only appends missing
// patterns) and best-effort: any failure is swallowed so it can never break sync.
func ensureExcludes(dir string) {
	p := filepath.Join(dir, ".git", "info", "exclude")
	existing, _ := os.ReadFile(p)
	have := map[string]bool{}
	for _, ln := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(ln)] = true
	}
	var add []string
	for _, pat := range osJunkPatterns {
		if !have[pat] {
			add = append(add, pat)
		}
	}
	if len(add) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "# SkillManage: never commit OS/editor junk to a shared skill repo\n" + strings.Join(add, "\n") + "\n"
	_ = os.WriteFile(p, []byte(body), 0o644)
}

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
	// Force proceeds even when the working tree is dirty. When false, a dirty tree
	// is surfaced via ErrDirty and left untouched (R26). HOW it proceeds depends on
	// KeepLocal.
	Force bool
	// KeepLocal changes a forced update from DISCARD to PRESERVE. When false (the
	// "discard local changes and align to upstream" path), a forced dirty update is
	// `reset --hard` + `clean -fd`. When true (the per-repo「仅更新」semantics), the
	// update stashes local changes, fast-forwards/merges upstream, then restores
	// them — never discarding; a conflict is reported as ActionConflict for the
	// user to resolve with git.
	KeepLocal bool
}

// Action describes what a sync did.
type Action string

const (
	ActionCloned    Action = "cloned"
	ActionSynced    Action = "synced"
	ActionDirtySkip Action = "dirty-skip"
	ActionConflict  Action = "conflict"
	ActionFailed    Action = "failed"
)

// ErrConflict is returned (as Result.Err) when a KeepLocal update could not
// integrate upstream without conflicting with local changes. The mirror is left
// for the user to resolve with git (never auto-discarded).
var ErrConflict = errors.New("local changes conflict with upstream update")

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
		ensureExcludes(dir) // fresh mirror: ignore OS junk from the start
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
		if opts.KeepLocal {
			// 仅更新: pull upstream while PRESERVING local add/delete/modify; a
			// conflict is reported (never discarded) for the user to resolve.
			return s.updateKeepingLocal(ctx, dir, ref, res)
		}
		// else: caller explicitly chose to discard local changes — fall through to
		// the hard align below.
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

// updateKeepingLocal integrates the upstream ref into a dirty mirror WITHOUT
// discarding local work (the「仅更新」semantics). It stashes working-tree changes
// (including untracked new skills), fast-forwards/merges the upstream ref, then
// restores the stashed changes. Non-conflicting upstream changes (e.g. a new
// skill added upstream) merge cleanly and the local add/delete/modify survives.
// Any conflict — merging commits or restoring the working tree — is reported as
// ActionConflict and left in place for the user to resolve with git; nothing is
// ever auto-discarded. A re-run of「同步仓库」can then upload the local changes.
func (s *Syncer) updateKeepingLocal(ctx context.Context, dir, ref string, res Result) Result {
	// Stash only when the working tree actually has changes (committed-unpushed
	// drift has a clean tree and nothing to stash). `status --porcelain` already
	// excludes the ignored OS junk, so it never stashes cruft.
	stashed := false
	if wt, _, err := s.run(ctx, dir, "status", "--porcelain"); err == nil && strings.TrimSpace(wt) != "" {
		if _, stderr, perr := s.run(ctx, dir, "stash", "push", "--include-untracked", "-m", "skillmanage-update"); perr != nil {
			res.Action, res.Err, res.Stderr = ActionFailed, fmt.Errorf("stash local changes: %w", perr), stderr
			return res
		}
		stashed = true
	}
	// Integrate upstream: fast-forwards when there are no local commits, otherwise
	// a merge commit. A merge conflict here can only come from committed-unpushed
	// local commits; abort and restore so the mirror is left clean for manual git.
	if _, stderr, err := s.run(ctx, dir, "merge", "--no-edit", ref); err != nil {
		_, _, _ = s.run(ctx, dir, "merge", "--abort")
		if stashed {
			_, _, _ = s.run(ctx, dir, "stash", "pop")
		}
		res.Action, res.Err, res.Stderr = ActionConflict, ErrConflict, strings.TrimSpace(stderr)
		return res
	}
	if stashed {
		// Reapply local changes on top of the updated upstream.
		if _, stderr, err := s.run(ctx, dir, "stash", "pop"); err != nil {
			// Local changes clash with the update — leave the conflict markers (and
			// the retained stash) for the user to resolve with git.
			res.Dirty = true
			res.Action, res.Err, res.Stderr = ActionConflict, ErrConflict, strings.TrimSpace(stderr)
			return res
		}
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
