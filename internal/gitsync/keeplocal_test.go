package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 仅更新 (KeepLocal) must pull a non-conflicting upstream change while keeping a
// local add — never discarding it (the old reset --hard behavior).
func TestUpdateKeepingLocalPreservesLocalAdd(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")
	if r := s.sync(context.Background(), dest, src, Options{Branch: "main"}); !r.OK {
		t.Fatalf("clone: %v %s", r.Err, r.Stderr)
	}

	// Local: a brand-new (untracked) skill in the mirror.
	if err := os.MkdirAll(filepath.Join(dest, "skill-local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "skill-local", "SKILL.md"), []byte("---\nname: skill-local\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Upstream: a DIFFERENT new skill, committed (upstream moves forward).
	if err := os.MkdirAll(filepath.Join(src, "skill-up"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skill-up", "SKILL.md"), []byte("---\nname: skill-up\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "add skill-up")

	r := s.sync(context.Background(), dest, src, Options{Branch: "main", Force: true, KeepLocal: true})
	if !r.OK || r.Action != ActionSynced {
		t.Fatalf("keep-local update: OK=%v action=%v err=%v stderr=%s", r.OK, r.Action, r.Err, r.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dest, "skill-local", "SKILL.md")); err != nil {
		t.Errorf("local add was discarded by 仅更新: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skill-up", "SKILL.md")); err != nil {
		t.Errorf("upstream add was not pulled in: %v", err)
	}
}

// When a local edit conflicts with an upstream edit of the same file, 仅更新 must
// report ActionConflict and must NOT discard the local change (leaves it for the
// user to resolve with git).
func TestUpdateKeepingLocalConflictReported(t *testing.T) {
	s := newTestSyncer(t)
	src := makeSourceRepo(t)
	dest := filepath.Join(t.TempDir(), "mirror")
	if r := s.sync(context.Background(), dest, src, Options{Branch: "main"}); !r.OK {
		t.Fatalf("clone: %v", r.Err)
	}

	// Local edit of a tracked file.
	if err := os.WriteFile(filepath.Join(dest, "skill-a", "SKILL.md"), []byte("---\nname: skill-a\nlocal: yes\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Upstream edits the SAME file differently + commits.
	if err := os.WriteFile(filepath.Join(src, "skill-a", "SKILL.md"), []byte("---\nname: skill-a\nupstream: yes\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "upstream edit")

	r := s.sync(context.Background(), dest, src, Options{Branch: "main", Force: true, KeepLocal: true})
	if r.Action != ActionConflict {
		t.Fatalf("expected ActionConflict, got action=%v OK=%v err=%v", r.Action, r.OK, r.Err)
	}
	// The local change must survive (conflict markers keep the local side).
	b, _ := os.ReadFile(filepath.Join(dest, "skill-a", "SKILL.md"))
	if !strings.Contains(string(b), "local: yes") {
		t.Errorf("local change was discarded on conflict; file=%q", string(b))
	}
}
