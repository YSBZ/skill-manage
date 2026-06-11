package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// makeSourceRepo creates a local git repo with one committed file and returns
// its path. It serves as a local "remote" for clone tests.
func makeSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skill-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill-a", "SKILL.md"), []byte("---\nname: skill-a\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func newTestSyncer(t *testing.T) *Syncer {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	s, err := NewSyncer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSyncCloneThenNoop(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")

	r := s.sync(context.Background(), dest, src, Options{Branch: "main"})
	if !r.OK || r.Action != ActionCloned {
		t.Fatalf("first sync: OK=%v action=%v err=%v stderr=%s", r.OK, r.Action, r.Err, r.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dest, "skill-a", "SKILL.md")); err != nil {
		t.Errorf("cloned mirror missing skill-a/SKILL.md: %v", err)
	}

	r = s.sync(context.Background(), dest, src, Options{Branch: "main"})
	if !r.OK || r.Action != ActionSynced {
		t.Fatalf("second sync: OK=%v action=%v err=%v stderr=%s", r.OK, r.Action, r.Err, r.Stderr)
	}
}

func TestSyncCleanRemovesUntracked(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")
	if r := s.sync(context.Background(), dest, src, Options{Branch: "main"}); !r.OK {
		t.Fatalf("clone failed: %v %s", r.Err, r.Stderr)
	}
	// Simulate a stray untracked dir+file left in the mirror.
	stray := filepath.Join(dest, "stray-skill")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := s.sync(context.Background(), dest, src, Options{Branch: "main", Force: true}); !r.OK {
		t.Fatalf("sync failed: %v %s", r.Err, r.Stderr)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("clean -fd should have removed untracked %s (err=%v)", stray, err)
	}
}

func TestSyncUpstreamDeleteRemovesSkill(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")
	if r := s.sync(context.Background(), dest, src, Options{Branch: "main"}); !r.OK {
		t.Fatalf("clone failed: %v %s", r.Err, r.Stderr)
	}
	// Upstream deletes skill-a.
	runGit(t, src, "rm", "-r", "-q", "skill-a")
	runGit(t, src, "commit", "-q", "-m", "remove skill-a")

	if r := s.sync(context.Background(), dest, src, Options{Branch: "main", Force: true}); !r.OK {
		t.Fatalf("sync failed: %v %s", r.Err, r.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dest, "skill-a")); !os.IsNotExist(err) {
		t.Errorf("upstream-deleted skill-a should be gone from mirror (err=%v)", err)
	}
}

func TestSyncDirtySkipThenForce(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")
	if r := s.sync(context.Background(), dest, src, Options{Branch: "main"}); !r.OK {
		t.Fatalf("clone failed: %v %s", r.Err, r.Stderr)
	}
	// Edit a tracked file in the mirror (simulates an in-place skill edit).
	readme := filepath.Join(dest, "README.md")
	if err := os.WriteFile(readme, []byte("LOCAL EDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without Force: surfaced as dirty, NOT discarded (R26).
	r := s.sync(context.Background(), dest, src, Options{Branch: "main"})
	if r.Action != ActionDirtySkip || r.Err != ErrDirty || !r.Dirty {
		t.Fatalf("dirty sync should skip: action=%v err=%v dirty=%v", r.Action, r.Err, r.Dirty)
	}
	if b, _ := os.ReadFile(readme); strings.TrimSpace(string(b)) != "LOCAL EDIT" {
		t.Errorf("dirty-skip should preserve local edit, got %q", b)
	}
	// With Force: drift discarded, mirror restored.
	r = s.sync(context.Background(), dest, src, Options{Branch: "main", Force: true})
	if !r.OK || r.Action != ActionSynced {
		t.Fatalf("forced sync should succeed: action=%v err=%v", r.Action, r.Err)
	}
	if b, _ := os.ReadFile(readme); strings.TrimSpace(string(b)) != "hello" {
		t.Errorf("forced sync should restore upstream content, got %q", b)
	}
}

func TestSyncMissingGitBinary(t *testing.T) {
	s := &Syncer{git: filepath.Join(t.TempDir(), "no-such-git"), hooksDir: t.TempDir()}
	dest := filepath.Join(t.TempDir(), "mirror")
	r := s.sync(context.Background(), dest, "/some/remote", Options{})
	if r.OK || r.Action != ActionFailed || r.Err == nil {
		t.Fatalf("missing git should fail cleanly: %+v", r)
	}
}

func TestSyncContextCancelled(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	r := s.sync(ctx, dest, src, Options{Branch: "main"})
	if r.OK {
		t.Fatalf("cancelled context should not produce a successful clone: %+v", r)
	}
}

// TestRunPassesDaemonSafeFlags uses a fake git to assert the hardening flags
// and core.hooksPath redirection (R25/KTD6) are actually passed.
func TestRunPassesDaemonSafeFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-git shell script is POSIX")
	}
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args")
	envFile := filepath.Join(tmp, "env")
	fakeGit := filepath.Join(tmp, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\nenv > \"" + envFile + "\"\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(tmp, "hooks")
	s := &Syncer{git: fakeGit, hooksDir: hooks}

	if _, _, err := s.run(context.Background(), "", "fetch", "--prune", "origin"); err != nil {
		t.Fatalf("run fake git: %v", err)
	}
	args, _ := os.ReadFile(argsFile)
	env, _ := os.ReadFile(envFile)

	if !strings.Contains(string(args), "core.hooksPath="+hooks) {
		t.Errorf("expected core.hooksPath=%s in args, got:\n%s", hooks, args)
	}
	for _, want := range []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
		"GCM_INTERACTIVE=never",
		"GIT_CONFIG_NOSYSTEM=1",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("expected env to contain %q", want)
		}
	}
}

// TestRunPerCommandTimeout proves a single git invocation is killed by the
// per-command timeout even when the caller's context has no deadline (the
// detached update-now case). A fake git that sleeps far longer than the
// timeout must return an error promptly, not hang.
func TestRunPerCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-git shell script is POSIX")
	}
	tmp := t.TempDir()
	fakeGit := filepath.Join(tmp, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{git: fakeGit, hooksDir: filepath.Join(tmp, "hooks"), cmdTimeout: 50 * time.Millisecond}

	start := time.Now()
	_, _, err := s.run(context.Background(), "", "fetch")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("run did not honor the per-command timeout (took %v)", elapsed)
	}
}
