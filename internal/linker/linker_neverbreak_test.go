package linker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

// Invariant ④ (never-break): a link SkillManage did not create — and that does
// not carry our private-store signature — is refused, never removed.
// Simulates a skills.sh install: ~/.<agent>/skills/x → ~/.agents/skills/x.
func TestLinkRefusesForeignLinkWithoutClobbering(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	// External canonical OUTSIDE our repos/local store (stands in for ~/.agents).
	external := filepath.Join(t.TempDir(), "agents", "skills", "find-skills")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(f.target, "find-skills")
	if err := os.Symlink(external, foreign); err != nil {
		t.Fatal(err)
	}

	// A desired link with the SAME name, sourced from our repo, must NOT clobber
	// the foreign link — it is refused as occupied.
	src := f.mkSource(t, "find-skills")
	_, err := f.mgr.Link(f.desired("find-skills", src), f.man)
	if !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("foreign link should be refused as occupied, got err=%v", err)
	}
	// The foreign link is untouched: still a symlink, still pointing at external.
	got, rlErr := os.Readlink(foreign)
	if rlErr != nil || filepath.Clean(got) != filepath.Clean(external) {
		t.Fatalf("foreign link was modified: target=%q err=%v", got, rlErr)
	}
	// And it was never recorded as ours.
	if findRecord(f.man, f.target, "find-skills") != nil {
		t.Fatal("foreign link must not be recorded in the manifest")
	}
}

// The looksOurs channel (KTD6(a)): an unowned link that DOES resolve under our
// private store is adopted (removed + recreated + recorded). This locks the
// chosen semantic so a future change cannot silently broaden or break it.
func TestLinkAdoptsOwnSignatureLink(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	src := f.mkSource(t, "ce-plan") // under reposRoot → our signature
	// A pre-existing link pointing under our repos root but NOT in the manifest
	// (e.g. manifest lost across a reinstall).
	stale := filepath.Join(f.target, "ce-plan")
	if err := os.Symlink(src, stale); err != nil {
		t.Fatal(err)
	}
	created, err := f.mgr.Link(f.desired("ce-plan", src), f.man)
	if err != nil {
		t.Fatalf("signature link should be adopted, got err=%v", err)
	}
	if !created {
		t.Fatal("adoption should report created=true")
	}
	if findRecord(f.man, f.target, "ce-plan") == nil {
		t.Fatal("adopted link must now be recorded in the manifest")
	}
}

// PruneDangling only ever iterates manifest records, so an on-disk link we never
// recorded (a foreign install) is invisible to it and survives prune untouched.
func TestPruneLeavesUnrecordedForeignLinkAlone(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "agents", "skills", "x")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(f.target, "x")
	if err := os.Symlink(external, foreign); err != nil {
		t.Fatal(err)
	}
	// Empty manifest → prune has nothing of ours to act on.
	removed, err := f.mgr.PruneDangling(&config.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("prune should remove nothing, got %+v", removed)
	}
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatalf("foreign link must survive prune, got %v", err)
	}
}
