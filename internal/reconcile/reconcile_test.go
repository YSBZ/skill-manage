package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillmanage/internal/config"
)

type fix struct {
	reposRoot     string
	personalStore string
	target        string
	rec           *Reconciler
	man           *config.Manifest
}

func newFix(t *testing.T) fix {
	t.Helper()
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	target := filepath.Join(root, "skills")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	personalStore := filepath.Join(root, "local")
	return fix{reposRoot: reposRoot, personalStore: personalStore, target: target, rec: New(reposRoot, personalStore), man: &config.Manifest{}}
}

// mkLocalSkill creates a skill in the @local personal store.
func (f fix) mkLocalSkill(t *testing.T, skill string) {
	t.Helper()
	dir := filepath.Join(f.personalStore, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+skill+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fix) mkSkill(t *testing.T, repo, skill string) {
	t.Helper()
	dir := filepath.Join(f.reposRoot, repo, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+skill+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fix) linkExists(skill string) bool {
	_, err := os.Stat(filepath.Join(f.target, skill, "SKILL.md"))
	return err == nil
}

func TestRepoWinsOverLocalSameName(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "alpha", "foo") // git repo alpha has foo
	f.mkLocalSkill(t, "foo")     // @local also has a foo
	cfg := config.Config{Enabled: []config.EnabledEntry{
		{Skill: "alpha/foo", Target: f.target, Mode: config.ModeSnapshot},
		{Skill: "@local/foo", Target: f.target, Mode: config.ModeSnapshot},
	}}
	sum := f.rec.Apply(cfg, f.man)
	if len(f.man.Links) != 1 {
		t.Fatalf("expected exactly 1 link (git wins over @local), got %d: %+v", len(f.man.Links), f.man.Links)
	}
	tgt, _ := os.Readlink(filepath.Join(f.target, "foo"))
	if !strings.Contains(tgt, filepath.Join("repos", "alpha", "foo")) {
		t.Errorf("link should point at the git repo source, got %q", tgt)
	}
	for _, c := range sum.Conflicts {
		if c.Kind == "collision" {
			t.Errorf("same-name git/@local should resolve by precedence, not raise a collision: %+v", c)
		}
	}
}

func TestApplyFollowAndSnapshot(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "alpha", "a1")
	f.mkSkill(t, "alpha", "a2")
	f.mkSkill(t, "beta", "b1")
	f.mkSkill(t, "beta", "b2")

	cfg := config.Config{Enabled: []config.EnabledEntry{
		{Skill: "alpha/*", Target: f.target, Mode: config.ModeFollow},
		{Skill: "beta/b1", Target: f.target, Mode: config.ModeSnapshot},
	}}
	sum := f.rec.Apply(cfg, f.man)
	if len(sum.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", sum.Errors)
	}
	for _, want := range []string{"a1", "a2", "b1"} {
		if !f.linkExists(want) {
			t.Errorf("expected link %s to exist", want)
		}
	}
	if f.linkExists("b2") {
		t.Errorf("b2 is not selected (snapshot only b1) and must not be linked")
	}
	if len(f.man.Links) != 3 {
		t.Errorf("manifest should have 3 links, got %d", len(f.man.Links))
	}
}

func TestFollowPicksUpNewSkill(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "alpha", "a1")
	cfg := config.Config{Enabled: []config.EnabledEntry{{Skill: "alpha/*", Target: f.target, Mode: config.ModeFollow}}}
	f.rec.Apply(cfg, f.man)

	// upstream adds a2
	f.mkSkill(t, "alpha", "a2")
	sum := f.rec.Apply(cfg, f.man)
	if !f.linkExists("a2") {
		t.Errorf("follow mode should auto-link new upstream skill a2")
	}
	var sawA2 bool
	for _, c := range sum.Created {
		if c.Name == "a2" {
			sawA2 = true
		}
	}
	if !sawA2 {
		t.Errorf("summary should report a2 as created (R9 visibility), got %+v", sum.Created)
	}
}

func TestSnapshotIgnoresNewSkill(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "beta", "b1")
	cfg := config.Config{Enabled: []config.EnabledEntry{{Skill: "beta/b1", Target: f.target, Mode: config.ModeSnapshot}}}
	f.rec.Apply(cfg, f.man)

	f.mkSkill(t, "beta", "b3")
	f.rec.Apply(cfg, f.man)
	if f.linkExists("b3") {
		t.Errorf("snapshot mode must not auto-link new skill b3")
	}
}

func TestDeselectRemovesLink(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "beta", "b1")
	cfg := config.Config{Enabled: []config.EnabledEntry{{Skill: "beta/b1", Target: f.target, Mode: config.ModeSnapshot}}}
	f.rec.Apply(cfg, f.man)
	if !f.linkExists("b1") {
		t.Fatal("b1 should be linked initially")
	}
	// deselect everything
	sum := f.rec.Apply(config.Config{}, f.man)
	if f.linkExists("b1") {
		t.Errorf("deselected b1 should be removed")
	}
	if len(sum.Removed) != 1 || sum.Removed[0].Name != "b1" {
		t.Errorf("summary should report b1 removed, got %+v", sum.Removed)
	}
	if len(f.man.Links) != 0 {
		t.Errorf("manifest should be empty after deselect, got %+v", f.man.Links)
	}
}

// TestMultiTargetFollowSyncsAddAndDelete locks the multi-tab sync behavior:
// when two directories (tabs) both follow the same repo, an upstream add lands
// in both, and an upstream delete clears the link from every tab.
func TestMultiTargetFollowSyncsAddAndDelete(t *testing.T) {
	f := newFix(t)
	t2 := filepath.Join(filepath.Dir(f.target), "skills2")
	f.mkSkill(t, "alpha", "a1")
	cfg := config.Config{Enabled: []config.EnabledEntry{
		{Skill: "alpha/*", Target: f.target, Mode: config.ModeFollow},
		{Skill: "alpha/*", Target: t2, Mode: config.ModeFollow},
	}}
	linkIn := func(dir, skill string) bool {
		_, err := os.Stat(filepath.Join(dir, skill, "SKILL.md"))
		return err == nil
	}
	f.rec.Apply(cfg, f.man)
	if !linkIn(f.target, "a1") || !linkIn(t2, "a1") {
		t.Fatal("a1 should link into both target tabs")
	}
	// upstream adds a2 → both following tabs pick it up
	f.mkSkill(t, "alpha", "a2")
	f.rec.Apply(cfg, f.man)
	if !linkIn(f.target, "a2") || !linkIn(t2, "a2") {
		t.Errorf("new upstream a2 should appear in both tabs")
	}
	// upstream deletes a1 → cleared from every tab
	if err := os.RemoveAll(filepath.Join(f.reposRoot, "alpha", "a1")); err != nil {
		t.Fatal(err)
	}
	f.rec.Apply(cfg, f.man)
	if linkIn(f.target, "a1") || linkIn(t2, "a1") {
		t.Errorf("deleted upstream a1 must be cleared from every tab")
	}
}

func TestUpstreamDeletePruned(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "alpha", "a1")
	f.mkSkill(t, "alpha", "a2")
	cfg := config.Config{Enabled: []config.EnabledEntry{{Skill: "alpha/*", Target: f.target, Mode: config.ModeFollow}}}
	f.rec.Apply(cfg, f.man)

	// upstream removes a1
	if err := os.RemoveAll(filepath.Join(f.reposRoot, "alpha", "a1")); err != nil {
		t.Fatal(err)
	}
	f.rec.Apply(cfg, f.man)
	if f.linkExists("a1") {
		t.Errorf("a1 link should be gone after upstream delete")
	}
	if !f.linkExists("a2") {
		t.Errorf("a2 should remain")
	}
	if len(f.man.Links) != 1 || f.man.Links[0].Name != "a2" {
		t.Errorf("manifest should only have a2, got %+v", f.man.Links)
	}
}

func TestNestedConflictCodexOnly(t *testing.T) {
	f := newFix(t)
	f.mkSkill(t, "alpha", "compound")
	// add a nested SKILL.md inside the skill dir → HasNested
	nested := filepath.Join(f.reposRoot, "alpha", "compound", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SKILL.md"), []byte("---\nname: child\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	codexTarget := filepath.Join(t.TempDir(), ".codex", "skills") // IsCodexTarget true (suffix)
	ccTarget := f.target                                          // plain dir → not Codex

	hasNested := func(sum Summary) bool {
		for _, c := range sum.Conflicts {
			if c.Kind == "nested" && c.LinkName == "compound" {
				return true
			}
		}
		return false
	}

	// Codex target → nested warning, link still created.
	sum := f.rec.Apply(config.Config{Enabled: []config.EnabledEntry{
		{Skill: "alpha/compound", Target: codexTarget, Mode: config.ModeSnapshot},
	}}, &config.Manifest{})
	if !hasNested(sum) {
		t.Errorf("Codex target with nested source should warn, got conflicts %+v", sum.Conflicts)
	}
	if _, err := os.Stat(filepath.Join(codexTarget, "compound", "SKILL.md")); err != nil {
		t.Errorf("nested warning must not block the link: %v", err)
	}

	// CC target → no nested warning.
	sum = f.rec.Apply(config.Config{Enabled: []config.EnabledEntry{
		{Skill: "alpha/compound", Target: ccTarget, Mode: config.ModeSnapshot},
	}}, &config.Manifest{})
	if hasNested(sum) {
		t.Errorf("CC target must not raise nested warning, got %+v", sum.Conflicts)
	}
}

func TestValidRepoName(t *testing.T) {
	for _, ok := range []string{"backend-skills", "fe.skills", "a_b"} {
		if !ValidRepoName(ok) {
			t.Errorf("ValidRepoName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../etc", "a/b", `a\b`, "../../root", "@local", "@anything"} {
		if ValidRepoName(bad) {
			t.Errorf("ValidRepoName(%q) = true, want false (traversal/reserved guard)", bad)
		}
	}
}

func TestLocalNamespaceResolves(t *testing.T) {
	f := newFix(t)
	// place a skill in the personal store (the @local root)
	store := f.rec.personalStore
	dir := filepath.Join(store, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := f.rec.Apply(config.Config{Enabled: []config.EnabledEntry{
		{Skill: "@local/mine", Target: f.target, Mode: config.ModeSnapshot},
	}}, f.man)
	if len(sum.Errors) != 0 {
		t.Fatalf("@local selector should resolve, got errors %v", sum.Errors)
	}
	if !f.linkExists("mine") {
		t.Errorf("@local/mine should link the personal-store skill")
	}
}

func TestApplyRejectsTraversalRepo(t *testing.T) {
	f := newFix(t)
	// a crafted enabled entry whose repo selector tries to escape reposRoot
	cfg := config.Config{Enabled: []config.EnabledEntry{
		{Skill: "../../../../etc/*", Target: f.target, Mode: config.ModeFollow},
	}}
	sum := f.rec.Apply(cfg, f.man)
	if len(sum.Errors) == 0 {
		t.Error("traversal repo selector should be rejected with an error")
	}
	if len(f.man.Links) != 0 {
		t.Errorf("no links should be created for a traversal selector, got %+v", f.man.Links)
	}
}

func TestRepoNameCollides(t *testing.T) {
	existing := []string{
		"git@github.com:teamA/skills.git",
		"https://example.com/group/ops-skills.git",
	}
	// Different host/path but identical last segment → same on-disk dir.
	if !RepoNameCollides(existing, "git@gitlab.com:teamB/skills.git") {
		t.Error("expected collision: teamB/skills maps to the same dir as teamA/skills")
	}
	// Identical URL is the ordinary duplicate case, not a collision.
	if RepoNameCollides(existing, "git@github.com:teamA/skills.git") {
		t.Error("identical URL must not count as a collision")
	}
	// A genuinely new, distinct name does not collide.
	if RepoNameCollides(existing, "https://example.com/team/frontend-skills.git") {
		t.Error("distinct repo name should not collide")
	}
	// .git suffix vs none still derives the same name → collision.
	if !RepoNameCollides(existing, "https://example.com/other/ops-skills") {
		t.Error("ops-skills (no .git) should collide with ops-skills.git")
	}
}

func TestRepoName(t *testing.T) {
	cases := map[string]string{
		"git@github.com:team/backend-skills.git":          "backend-skills",
		"https://example.com/team/frontend-skills.git":    "frontend-skills",
		"https://example.com/x":                           "x",
		"ssh://git@example.com/group/sub/ops-skills.git/": "ops-skills",
	}
	for url, want := range cases {
		if got := RepoName(url); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", url, got, want)
		}
	}
}
