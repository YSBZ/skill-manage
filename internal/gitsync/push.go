package gitsync

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// PushError classifies a push failure with a stable Code the API/UI maps to a
// user-facing message. Stderr is credential-scrubbed before it leaves here.
type PushError struct {
	Code   string // push_auth | push_rejected | push_network | push_failed
	Stderr string // scrubbed git stderr (no embedded credentials)
	Err    error
}

func (e *PushError) Error() string {
	if e.Stderr != "" {
		return e.Code + ": " + e.Stderr
	}
	return e.Code + ": " + e.Err.Error()
}
func (e *PushError) Unwrap() error { return e.Err }

// credInURL matches an HTTPS remote with inline user:token credentials, which
// git can echo back in a push error (e.g. "fatal: ... https://u:tok@host/..").
// We never let that reach the HTTP layer or logs.
var credInURL = regexp.MustCompile(`(https?://)[^:@/\s]+:[^@/\s]+@`)

// ScrubCreds redacts inline HTTPS credentials from text.
func ScrubCreds(s string) string { return credInURL.ReplaceAllString(s, `$1***:***@`) }

// ResolveBranch returns the branch to push to. It prefers the configured branch
// (the same one Sync's reset targets, keeping "push succeeds → next reset
// aligns losslessly" true); when empty it reads the remote's default branch
// from `symbolic-ref refs/remotes/origin/HEAD`. It deliberately never uses
// `rev-parse --abbrev-ref HEAD`: a reset --hard mirror is frequently in a
// detached-HEAD state with no local tracking branch, where that yields "HEAD".
func (s *Syncer) ResolveBranch(ctx context.Context, dir, configured string) (string, error) {
	if b := strings.TrimSpace(configured); b != "" {
		return b, nil
	}
	out, stderr, err := s.run(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return strings.TrimPrefix(b, "origin/"), nil
		}
	}
	e := strings.TrimSpace(stderr)
	if e == "" && err != nil {
		e = err.Error()
	}
	return "", fmt.Errorf("resolve default branch (origin/HEAD): %s", e)
}

// Add stages the given repo-relative pathspecs.
func (s *Syncer) Add(ctx context.Context, dir string, paths ...string) error {
	args := append([]string{"add", "--"}, paths...)
	if _, stderr, err := s.runEnv(ctx, dir, []string{"LC_ALL=C"}, args...); err != nil {
		if e := strings.TrimSpace(stderr); e != "" {
			return fmt.Errorf("git add: %s", e)
		}
		return fmt.Errorf("git add: %w", err)
	}
	return nil
}

// Commit creates a commit with message. committed is false (no error) when the
// index had nothing staged — git refuses an empty commit, and that is a benign
// no-op for the push-retry path (commit already exists, only the push failed).
func (s *Syncer) Commit(ctx context.Context, dir, message string) (committed bool, err error) {
	stdout, stderr, err := s.runEnv(ctx, dir, []string{"LC_ALL=C"}, "commit", "-m", message)
	if err == nil {
		return true, nil
	}
	combined := stdout + " " + stderr
	if strings.Contains(combined, "nothing to commit") || strings.Contains(combined, "no changes added") {
		return false, nil
	}
	if e := strings.TrimSpace(stderr); e != "" {
		return false, fmt.Errorf("git commit: %s", e)
	}
	return false, fmt.Errorf("git commit: %w", err)
}

// AheadOfUpstream reports whether local HEAD has commits not on origin/<branch>
// (i.e. there is something to push). It is purely local (no network) and is the
// commit-aware half of drift detection: a clean working tree can still be ahead.
func (s *Syncer) AheadOfUpstream(ctx context.Context, dir, branch string) (bool, error) {
	out, stderr, err := s.run(ctx, dir, "rev-list", "--count", "origin/"+branch+"..HEAD")
	if err != nil {
		e := strings.TrimSpace(stderr)
		if e == "" {
			e = err.Error()
		}
		return false, fmt.Errorf("rev-list ahead-count: %s", e)
	}
	return strings.TrimSpace(out) != "0" && strings.TrimSpace(out) != "", nil
}

// Push pushes local HEAD to refs/heads/<branch> on origin, reusing the stored
// HTTPS credentials via GIT_ASKPASS. It never force-pushes. On failure it
// classifies the cause (auth / non-fast-forward / network) and returns a
// *PushError with credential-scrubbed stderr; the local commit is left intact
// for the user to retry after resolving the cause (R5).
func (s *Syncer) Push(ctx context.Context, dir, branch string) error {
	refspec := "HEAD:refs/heads/" + branch
	_, stderr, err := s.runEnv(ctx, dir, []string{"LC_ALL=C"}, "push", "origin", refspec)
	if err == nil {
		return nil
	}
	clean := ScrubCreds(stderr)
	return &PushError{Code: classifyPushErr(clean), Stderr: strings.TrimSpace(clean), Err: err}
}

// AlignWorkingTree resets the working tree to origin/<branch> after a
// successful push. A push updates the local remote-tracking ref to the pushed
// commit, so this is a no-op in the normal case — its purpose is to clear any
// .gitattributes (autocrlf/text) normalization ghost that would otherwise leave
// the tree spuriously "dirty" and trip the next sync's drift guard. Best-effort.
func (s *Syncer) AlignWorkingTree(ctx context.Context, dir, branch string) error {
	if _, stderr, err := s.run(ctx, dir, "reset", "--hard", "origin/"+branch); err != nil {
		if e := strings.TrimSpace(stderr); e != "" {
			return fmt.Errorf("align working tree: %s", e)
		}
		return fmt.Errorf("align working tree: %w", err)
	}
	return nil
}

// classifyPushErr maps git's (LC_ALL=C, English) push stderr to a stable code.
func classifyPushErr(stderr string) string {
	l := strings.ToLower(stderr)
	switch {
	case strings.Contains(l, "non-fast-forward") ||
		strings.Contains(l, "! [rejected]") ||
		strings.Contains(l, "failed to push some refs") && strings.Contains(l, "fetch first"):
		return "push_rejected"
	case strings.Contains(l, "403") ||
		strings.Contains(l, "permission") && strings.Contains(l, "denied") ||
		strings.Contains(l, "authentication failed") ||
		strings.Contains(l, "could not read username") ||
		strings.Contains(l, "could not read password") ||
		strings.Contains(l, "access denied"):
		return "push_auth"
	case strings.Contains(l, "could not resolve host") ||
		strings.Contains(l, "couldn't connect") ||
		strings.Contains(l, "could not connect") ||
		strings.Contains(l, "connection timed out") ||
		strings.Contains(l, "network is unreachable") ||
		strings.Contains(l, "timed out") ||
		strings.Contains(l, "operation timed out"):
		return "push_network"
	default:
		// "failed to push some refs" with no clearer cue is most often a
		// non-fast-forward; but without a definite marker, stay honest.
		if strings.Contains(l, "failed to push some refs") {
			return "push_rejected"
		}
		return "push_failed"
	}
}
