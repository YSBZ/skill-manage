package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeBareRemote creates a bare repo seeded with one commit on main and returns
// its path. A bare repo accepts pushes, so it stands in for the upstream.
func makeBareRemote(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", bare)
	seed := t.TempDir()
	runGit(t, seed, "clone", "-q", bare, ".")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "-A")
	runGit(t, seed, "commit", "-q", "-m", "seed")
	runGit(t, seed, "push", "-q", "origin", "main")
	return bare
}

// cloneMirror clones bare into a fresh dir with a local commit identity set, so
// our Syncer.Commit (which does not inject identity) works hermetically.
func cloneMirror(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mirror")
	runGit(t, t.TempDir(), "clone", "-q", bare, dir)
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	return dir
}

func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBranch(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	ctx := context.Background()

	if b, err := s.ResolveBranch(ctx, mirror, "feature"); err != nil || b != "feature" {
		t.Fatalf("configured branch: got %q err=%v, want feature", b, err)
	}
	if b, err := s.ResolveBranch(ctx, mirror, ""); err != nil || b != "main" {
		t.Fatalf("default branch from origin/HEAD: got %q err=%v, want main", b, err)
	}
}

func TestResolveBranchDetachedHEAD(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	// Detach HEAD onto the current commit — abbrev-ref HEAD would yield "HEAD".
	runGit(t, mirror, "checkout", "-q", "--detach")
	if b, err := s.ResolveBranch(context.Background(), mirror, ""); err != nil || b != "main" {
		t.Fatalf("detached HEAD: got %q err=%v, want main (from origin/HEAD)", b, err)
	}
}

func TestAddCommitPushRoundTrip(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	ctx := context.Background()

	writeSkill(t, mirror, "skill-b")
	if err := s.Add(ctx, mirror, "skill-b"); err != nil {
		t.Fatalf("add: %v", err)
	}
	committed, err := s.Commit(ctx, mirror, "skill-b a contributed skill")
	if err != nil || !committed {
		t.Fatalf("commit: committed=%v err=%v", committed, err)
	}
	ahead, err := s.AheadOfUpstream(ctx, mirror, "main")
	if err != nil || !ahead {
		t.Fatalf("ahead before push: ahead=%v err=%v, want true", ahead, err)
	}
	if err := s.Push(ctx, mirror, "main"); err != nil {
		t.Fatalf("push: %v", err)
	}
	// After a successful push, HEAD no longer leads origin/main.
	if ahead, _ := s.AheadOfUpstream(ctx, mirror, "main"); ahead {
		t.Errorf("still ahead after push")
	}
	// The bare remote now carries skill-b.
	verify := filepath.Join(t.TempDir(), "verify")
	runGit(t, t.TempDir(), "clone", "-q", bare, verify)
	if _, err := os.Stat(filepath.Join(verify, "skill-b", "SKILL.md")); err != nil {
		t.Errorf("pushed skill-b missing on remote: %v", err)
	}
}

func TestCommitNothingStaged(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	committed, err := s.Commit(context.Background(), mirror, "noop nothing")
	if err != nil {
		t.Fatalf("commit with nothing staged should not error: %v", err)
	}
	if committed {
		t.Errorf("committed=true with nothing staged")
	}
}

func TestPushNonFastForward(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	ctx := context.Background()

	// Another clone advances origin/main, so the mirror's later commit is no
	// longer a fast-forward.
	other := t.TempDir()
	runGit(t, other, "clone", "-q", bare, ".")
	runGit(t, other, "config", "user.email", "o@example.com")
	runGit(t, other, "config", "user.name", "o")
	if err := os.WriteFile(filepath.Join(other, "CHANGELOG.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-q", "-m", "advance")
	runGit(t, other, "push", "-q", "origin", "main")

	// Mirror commits on top of the now-stale main and pushes → rejected.
	writeSkill(t, mirror, "skill-c")
	if err := s.Add(ctx, mirror, "skill-c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, mirror, "skill-c stale"); err != nil {
		t.Fatal(err)
	}
	err := s.Push(ctx, mirror, "main")
	if err == nil {
		t.Fatal("expected push rejection")
	}
	pe, ok := err.(*PushError)
	if !ok {
		t.Fatalf("want *PushError, got %T: %v", err, err)
	}
	if pe.Code != "push_rejected" {
		t.Errorf("want push_rejected, got %q (stderr=%q)", pe.Code, pe.Stderr)
	}
	// The local commit survives a rejected push.
	if ahead, _ := s.AheadOfUpstream(ctx, mirror, "main"); !ahead {
		t.Errorf("local commit lost after rejected push")
	}
}

func TestClassifyPushErr(t *testing.T) {
	cases := []struct {
		stderr, want string
	}{
		{"remote: Permission to org/repo.git denied to user.\nfatal: unable to access 'https://github.com/org/repo.git/': The requested URL returned error: 403", "push_auth"},
		{"fatal: Authentication failed for 'https://github.com/org/repo.git/'", "push_auth"},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", "push_auth"},
		{" ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to 'origin'\nhint: Updates were rejected because the tip of your current branch is behind", "push_rejected"},
		{"fatal: unable to access 'https://github.com/org/repo.git/': Could not resolve host: github.com", "push_network"},
		{"fatal: unable to access 'https://github.com/org/repo.git/': Failed to connect to github.com port 443: Operation timed out", "push_network"},
		{"error: failed to push some refs to 'origin'", "push_rejected"},
		{"some unrecognized git failure", "push_failed"},
	}
	for _, c := range cases {
		if got := classifyPushErr(c.stderr); got != c.want {
			t.Errorf("classify(%q) = %q, want %q", c.stderr, got, c.want)
		}
	}
}

func TestScrubCreds(t *testing.T) {
	in := "fatal: unable to access 'https://alice:ghp_secrettoken123@github.com/org/repo.git/': 403"
	out := ScrubCreds(in)
	if got := out; got == in {
		t.Errorf("credentials not scrubbed: %q", got)
	}
	for _, leak := range []string{"alice", "ghp_secrettoken123"} {
		if containsSub(ScrubCreds(in), leak) {
			t.Errorf("scrubbed output still contains %q: %q", leak, ScrubCreds(in))
		}
	}
	if !containsSub(ScrubCreds(in), "github.com/org/repo.git") {
		t.Errorf("scrub removed the host/path too: %q", ScrubCreds(in))
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
