package linker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

type fixture struct {
	reposRoot     string
	target        string
	personalStore string
	mgr           *Manager
	man           *config.Manifest
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	target := filepath.Join(root, "skills")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	personalStore := filepath.Join(root, "local")
	return fixture{reposRoot: reposRoot, target: target, personalStore: personalStore, mgr: NewManager(reposRoot, personalStore), man: &config.Manifest{}}
}

// mkSource creates a skill dir under reposRoot and returns its abs path.
func (f fixture) mkSource(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(f.reposRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (f fixture) desired(name, source string) DesiredLink {
	return DesiredLink{LinkName: name, Target: f.target, Source: source}
}

func TestLinkCreateAndIdempotent(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	d := f.desired("ce-plan", src)

	created, err := f.mgr.Link(d, f.man)
	if err != nil || !created {
		t.Fatalf("first Link: created=%v err=%v", created, err)
	}
	tp := filepath.Join(f.target, "ce-plan")
	if _, err := os.Stat(filepath.Join(tp, "SKILL.md")); err != nil {
		t.Fatalf("link does not resolve to source: %v", err)
	}
	if len(f.man.Links) != 1 || f.man.Links[0].LinkType != config.LinkSymlink {
		t.Fatalf("manifest not recorded correctly: %+v", f.man.Links)
	}
	// idempotent: second Link is a no-op
	created, err = f.mgr.Link(d, f.man)
	if err != nil || created {
		t.Fatalf("second Link should be no-op: created=%v err=%v", created, err)
	}
	if len(f.man.Links) != 1 {
		t.Fatalf("manifest should still have 1 entry, got %d", len(f.man.Links))
	}
}

func TestLinkRealDirConflict(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	// a real dir already occupies the target name, not created by us
	occupied := filepath.Join(f.target, "ce-plan")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "mine.txt"), []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.mgr.Link(f.desired("ce-plan", src), f.man)
	if !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("want ErrTargetOccupied, got %v", err)
	}
	// untouched
	if _, err := os.Stat(filepath.Join(occupied, "mine.txt")); err != nil {
		t.Errorf("real dir should be left untouched: %v", err)
	}
}

func TestLinkDivergedOwnedButRealDir(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	// manifest claims ownership, but on disk it's a real dir
	f.man.Links = append(f.man.Links, config.LinkRecord{Name: "ce-plan", Target: f.target, Source: src, LinkType: config.LinkSymlink})
	realDir := filepath.Join(f.target, "ce-plan")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := f.mgr.Link(f.desired("ce-plan", src), f.man)
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("want ErrDiverged, got %v", err)
	}
}

func TestLinkForeignSymlinkRejected(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	// a symlink not created by us, pointing OUTSIDE reposRoot
	foreign := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(f.target, "ce-plan")); err != nil {
		t.Fatal(err)
	}
	_, err := f.mgr.Link(f.desired("ce-plan", src), f.man)
	if !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("foreign symlink should be rejected with ErrTargetOccupied, got %v", err)
	}
}

func TestLinkAdoptSignatureLink(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	// an unowned symlink that points UNDER reposRoot — our signature → adopt
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, filepath.Join(f.target, "ce-plan")); err != nil {
		t.Fatal(err)
	}
	created, err := f.mgr.Link(f.desired("ce-plan", src), f.man)
	if err != nil || !created {
		t.Fatalf("signature link should be adopted/recreated: created=%v err=%v", created, err)
	}
	if len(f.man.Links) != 1 {
		t.Fatalf("adopted link should be recorded, manifest=%+v", f.man.Links)
	}
}

func TestLinkReplaceStaleSource(t *testing.T) {
	f := newFixture(t)
	src1 := f.mkSource(t, "old")
	src2 := f.mkSource(t, "new")
	// owned link pointing at src1
	if _, err := f.mgr.Link(DesiredLink{LinkName: "ce-plan", Target: f.target, Source: src1}, f.man); err != nil {
		t.Fatal(err)
	}
	// re-link same name to a different source → replace
	created, err := f.mgr.Link(DesiredLink{LinkName: "ce-plan", Target: f.target, Source: src2}, f.man)
	if err != nil || !created {
		t.Fatalf("re-link to new source should recreate: created=%v err=%v", created, err)
	}
	if !linkPointsAt(filepath.Join(f.target, "ce-plan"), src2) {
		t.Errorf("link should now point at src2")
	}
	if len(f.man.Links) != 1 || f.man.Links[0].Source != src2 {
		t.Errorf("manifest should record src2: %+v", f.man.Links)
	}
}

func TestLinkRecopiesOwnedCopy(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	// Simulate an existing owned copy: a real dir at the target + a copy record.
	tp := filepath.Join(f.target, "ce-plan")
	if err := os.MkdirAll(tp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tp, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.man.Links = append(f.man.Links, config.LinkRecord{Name: "ce-plan", Target: f.target, Source: src, LinkType: config.LinkCopy})

	// Link must treat the copy as owned and refresh it (KTD12), not ErrDiverged.
	created, err := f.mgr.Link(f.desired("ce-plan", src), f.man)
	if err != nil {
		t.Fatalf("owned copy should be refreshed, got err: %v", err)
	}
	if !created {
		t.Fatal("re-copy should report created=true")
	}
	if _, err := os.Stat(filepath.Join(tp, "stale.txt")); !os.IsNotExist(err) {
		t.Errorf("stale copy content should be gone after refresh: %v", err)
	}
}

func TestUnlink(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	if _, err := f.mgr.Link(f.desired("ce-plan", src), f.man); err != nil {
		t.Fatal(err)
	}
	rec := f.man.Links[0]
	if err := f.mgr.Unlink(rec, f.man); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.target, "ce-plan")); !os.IsNotExist(err) {
		t.Errorf("link should be gone: %v", err)
	}
	if len(f.man.Links) != 0 {
		t.Errorf("manifest should be empty, got %+v", f.man.Links)
	}
}

func TestPruneDanglingSourceGone(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	if _, err := f.mgr.Link(f.desired("ce-plan", src), f.man); err != nil {
		t.Fatal(err)
	}
	// upstream removed the source skill dir
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	removed, err := f.mgr.PruneDangling(f.man)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Name != "ce-plan" {
		t.Fatalf("expected ce-plan pruned, got %+v", removed)
	}
	if _, err := os.Lstat(filepath.Join(f.target, "ce-plan")); !os.IsNotExist(err) {
		t.Errorf("dangling link should be removed from disk: %v", err)
	}
	if len(f.man.Links) != 0 {
		t.Errorf("manifest should drop pruned link, got %+v", f.man.Links)
	}
}

func TestPruneDanglingLinkGone(t *testing.T) {
	f := newFixture(t)
	src := f.mkSource(t, "ce-plan")
	if _, err := f.mgr.Link(f.desired("ce-plan", src), f.man); err != nil {
		t.Fatal(err)
	}
	// the link itself was removed out-of-band, source still present
	if err := os.Remove(filepath.Join(f.target, "ce-plan")); err != nil {
		t.Fatal(err)
	}
	removed, err := f.mgr.PruneDangling(f.man)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("expected 1 pruned (link gone), got %+v", removed)
	}
	if len(f.man.Links) != 0 {
		t.Errorf("manifest should drop record for gone link, got %+v", f.man.Links)
	}
}

// TestPruneDanglingCopyDivergedNotClobbered guards the RemoveAll path: a record
// claims a copy we own, but the on-disk path diverged into a plain file. Even
// with the source gone, PruneDangling must refuse the destructive RemoveAll and
// keep the record so the divergence stays visible (KTD5 never-clobber).
func TestPruneDanglingCopyDivergedNotClobbered(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(f.target, "ce-plan")
	if err := os.WriteFile(tp, []byte("not our copy dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Manifest records a copy whose source no longer exists.
	f.man.Links = append(f.man.Links, config.LinkRecord{
		Name: "ce-plan", Target: f.target,
		Source: filepath.Join(f.reposRoot, "gone", "ce-plan"), LinkType: config.LinkCopy,
	})
	removed, err := f.mgr.PruneDangling(f.man)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Errorf("diverged non-dir copy must not be pruned, got %+v", removed)
	}
	if _, err := os.Stat(tp); err != nil {
		t.Errorf("diverged path must be left untouched: %v", err)
	}
	if len(f.man.Links) != 1 {
		t.Errorf("record should be kept so divergence stays visible, got %+v", f.man.Links)
	}
}

// TestLooksOursPersonalStore guards the adopt re-entry path: an in-place link
// pointing into the personal store must be recognized as ours even with an
// empty manifest, so re-adoption does not mistake it for a foreign link.
func TestLooksOursPersonalStore(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(f.personalStore, "adopted")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: adopted\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// link with an empty manifest — must be adopted via the personalStore signature
	created, err := f.mgr.Link(f.desired("adopted", src), &config.Manifest{})
	if err != nil {
		t.Fatalf("link into target from personalStore source should succeed: %v", err)
	}
	if !created {
		t.Errorf("expected a link to be created")
	}
	if !f.mgr.looksOurs(filepath.Join(f.target, "adopted")) {
		t.Errorf("a link pointing into personalStore must be recognized as ours")
	}
}

func TestDetectConflicts(t *testing.T) {
	desired := []DesiredLink{
		// collision: same target+name, two sources
		{LinkName: "dup", Target: "/skills", Source: "/repos/a/dup"},
		{LinkName: "dup", Target: "/skills", Source: "/repos/b/dup"},
		// shadow: same name under two targets
		{LinkName: "shadowed", Target: "/skills", Source: "/repos/a/shadowed"},
		{LinkName: "shadowed", Target: "/proj/.claude/skills", Source: "/repos/a/shadowed"},
		// clean
		{LinkName: "fine", Target: "/skills", Source: "/repos/a/fine"},
	}
	conflicts := DetectConflicts(desired)
	var gotCollision, gotShadow bool
	for _, c := range conflicts {
		switch c.Kind {
		case ConflictCollision:
			if c.LinkName == "dup" && len(c.Sources) == 2 {
				gotCollision = true
			}
		case ConflictShadow:
			if c.LinkName == "shadowed" && len(c.Targets) == 2 {
				gotShadow = true
			}
		}
	}
	if !gotCollision {
		t.Errorf("expected a collision conflict for 'dup', got %+v", conflicts)
	}
	if !gotShadow {
		t.Errorf("expected a shadow conflict for 'shadowed', got %+v", conflicts)
	}
	// 'fine' must not appear
	for _, c := range conflicts {
		if c.LinkName == "fine" {
			t.Errorf("'fine' should not be a conflict: %+v", c)
		}
	}
}
