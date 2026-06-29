package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// driftKindFor returns the recorded kind for skill in d, or "".
func driftKindFor(d Drift, skill string) DriftKind {
	if k, ok := d.Has(skill); ok {
		return k
	}
	return ""
}

func TestSyncCleanStillResets(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	// A pristine mirror has no drift → sync reset-aligns as before (behavior
	// unchanged for the clean path).
	r := s.sync(context.Background(), mirror, bare, Options{Branch: "main"})
	if !r.OK || r.Action != ActionSynced || r.Dirty {
		t.Fatalf("clean mirror: OK=%v action=%v dirty=%v err=%v", r.OK, r.Action, r.Dirty, r.Err)
	}
}

func TestSyncYieldsOnUntrackedAdd(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	writeSkill(t, mirror, "skill-new") // untracked, never committed

	r := s.sync(context.Background(), mirror, bare, Options{Branch: "main"})
	if r.Action != ActionDirtySkip || !r.Dirty {
		t.Fatalf("untracked add: action=%v dirty=%v, want dirty-skip", r.Action, r.Dirty)
	}
	if _, err := os.Stat(filepath.Join(mirror, "skill-new", "SKILL.md")); err != nil {
		t.Errorf("untracked skill deleted by sync: %v", err)
	}
	if k := driftKindFor(r.Drift, "skill-new"); k != DriftAdded {
		t.Errorf("drift kind = %q, want added", k)
	}
}

func TestSyncYieldsOnCommittedUnpushed(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	ctx := context.Background()
	// Contribute-then-push-failed shape: commit a new skill but never push, so
	// the working tree is CLEAN and only the commit is ahead. This is the P0
	// state porcelain alone cannot see.
	writeSkill(t, mirror, "skill-committed")
	runGit(t, mirror, "add", "-A")
	runGit(t, mirror, "commit", "-q", "-m", "skill-committed x")

	r := s.sync(ctx, mirror, bare, Options{Branch: "main"})
	if r.Action != ActionDirtySkip || !r.Dirty {
		t.Fatalf("committed-unpushed: action=%v dirty=%v, want dirty-skip (NOT reset)", r.Action, r.Dirty)
	}
	if _, err := os.Stat(filepath.Join(mirror, "skill-committed", "SKILL.md")); err != nil {
		t.Errorf("committed-unpushed skill destroyed by sync: %v", err)
	}
	if k := driftKindFor(r.Drift, "skill-committed"); k != DriftCommitted {
		t.Errorf("drift kind = %q, want committed", k)
	}
}

func TestSyncForceDiscardsDrift(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	writeSkill(t, mirror, "skill-doomed")

	r := s.sync(context.Background(), mirror, bare, Options{Branch: "main", Force: true})
	if !r.OK || r.Action != ActionSynced {
		t.Fatalf("force: action=%v err=%v, want synced", r.Action, r.Err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "skill-doomed")); !os.IsNotExist(err) {
		t.Errorf("Force=true should have discarded the untracked add")
	}
}

func TestSyncMixedDrift(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	ctx := context.Background()
	// One skill committed-unpushed, README modified in the working tree.
	writeSkill(t, mirror, "skill-c2")
	runGit(t, mirror, "add", "-A")
	runGit(t, mirror, "commit", "-q", "-m", "skill-c2 x")
	if err := os.WriteFile(filepath.Join(mirror, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := s.sync(ctx, mirror, bare, Options{Branch: "main"})
	if r.Action != ActionDirtySkip {
		t.Fatalf("mixed drift: action=%v, want dirty-skip", r.Action)
	}
	if k := driftKindFor(r.Drift, "skill-c2"); k != DriftCommitted {
		t.Errorf("skill-c2 kind = %q, want committed", k)
	}
	if k := driftKindFor(r.Drift, "README.md"); k != DriftModified {
		t.Errorf("README.md kind = %q, want modified", k)
	}
}

func TestCheckUpdateRobustToLocalCommits(t *testing.T) {
	s := newTestSyncer(t)
	bare := makeBareRemote(t)
	mirror := cloneMirror(t, bare)
	ctx := context.Background()
	// Local unpushed commit must NOT be mistaken for an upstream update.
	writeSkill(t, mirror, "skill-local")
	runGit(t, mirror, "add", "-A")
	runGit(t, mirror, "commit", "-q", "-m", "local only")

	upd, err := s.CheckUpdate(ctx, mirror, "main")
	if err != nil {
		t.Fatalf("check update: %v", err)
	}
	if upd {
		t.Errorf("local commit reported as an available upstream update (false positive)")
	}
}
