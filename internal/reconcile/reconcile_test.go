package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

type fix struct {
	reposRoot string
	target    string
	rec       *Reconciler
	man       *config.Manifest
}

func newFix(t *testing.T) fix {
	t.Helper()
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	target := filepath.Join(root, "skills")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return fix{reposRoot: reposRoot, target: target, rec: New(reposRoot), man: &config.Manifest{}}
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
	for _, bad := range []string{"", ".", "..", "../etc", "a/b", `a\b`, "../../root"} {
		if ValidRepoName(bad) {
			t.Errorf("ValidRepoName(%q) = true, want false (traversal guard)", bad)
		}
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
