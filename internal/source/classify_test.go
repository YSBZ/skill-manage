package source

import (
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/linker"
	"skillmanage/internal/scanner"
)

type clfix struct {
	root, repos, store, target, agents, agentsSkills, plugins string
	mgr                                                       *linker.Manager
	man                                                       *config.Manifest
}

func newClfix(t *testing.T) clfix {
	t.Helper()
	root := t.TempDir()
	f := clfix{
		root:         root,
		repos:        filepath.Join(root, "repos"),
		store:        filepath.Join(root, "local"),
		target:       filepath.Join(root, "claude", "skills"),
		agents:       filepath.Join(root, "agents"),
		agentsSkills: filepath.Join(root, "agents", "skills"),
		plugins:      filepath.Join(root, "claude", "plugins"),
	}
	for _, d := range []string{f.repos, f.store, f.target, f.agentsSkills, f.plugins} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.mgr = linker.NewManager(f.repos, f.store)
	f.man = &config.Manifest{}
	return f
}

func (f clfix) classifier(lock SkillLock) Classifier {
	return Classifier{
		Mgr:              f.mgr,
		Manifest:         f.man,
		AgentsSkillsRoot: f.agentsSkills,
		Lock:             lock,
		PluginRoots:      []string{f.plugins},
		AllowedRoots:     []string{f.repos, f.store, f.agentsSkills, f.plugins},
	}
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func symlink(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.Symlink(src, dst); err != nil {
		t.Fatal(err)
	}
}

func entry(name, dir string) scanner.Skill { return scanner.Skill{LinkName: name, Dir: dir} }

func TestClassifyAllKinds(t *testing.T) {
	f := newClfix(t)

	// git (manifest-owned symlink)
	gsrc := mkdir(t, filepath.Join(f.repos, "myrepo", "ce-plan"))
	gdir := filepath.Join(f.target, "ce-plan")
	symlink(t, gsrc, gdir)
	f.man.Links = append(f.man.Links, config.LinkRecord{Name: "ce-plan", Target: f.target, Source: gsrc, LinkType: config.LinkSymlink})

	// local (manifest-owned symlink)
	lsrc := mkdir(t, filepath.Join(f.store, "my-local"))
	ldir := filepath.Join(f.target, "my-local")
	symlink(t, lsrc, ldir)
	f.man.Links = append(f.man.Links, config.LinkRecord{Name: "my-local", Target: f.target, Source: lsrc, LinkType: config.LinkSymlink})

	// copy-fallback real dir (manifest-owned, LinkType=copy) → must be git, not handwritten
	cdir := mkdir(t, filepath.Join(f.target, "copied"))
	f.man.Links = append(f.man.Links, config.LinkRecord{Name: "copied", Target: f.target, Source: filepath.Join(f.repos, "myrepo", "copied"), LinkType: config.LinkCopy})

	// skills.sh (symlink into ~/.agents/skills + lockfile hit)
	ssrc := mkdir(t, filepath.Join(f.agentsSkills, "find-skills"))
	sdir := filepath.Join(f.target, "find-skills")
	symlink(t, ssrc, sdir)

	// plugin (symlink into plugins tree)
	psrc := mkdir(t, filepath.Join(f.plugins, "cache", "some-plugin"))
	pdir := filepath.Join(f.target, "plug")
	symlink(t, psrc, pdir)

	// handwritten (real dir, not in manifest)
	hdir := mkdir(t, filepath.Join(f.target, "hand-made"))

	// signature-owned (symlink under repos, NO manifest record)
	sigsrc := mkdir(t, filepath.Join(f.repos, "otherrepo", "sig"))
	sigdir := filepath.Join(f.target, "sig")
	symlink(t, sigsrc, sigdir)

	lock := SkillLock{Skills: map[string]LockEntry{"find-skills": {Source: "vercel-labs/skills", SourceURL: "https://github.com/vercel-labs/skills.git"}}}
	c := f.classifier(lock)

	check := func(name, dir string, wantKind SourceKind, wantRepo string) {
		t.Helper()
		got := c.Classify(entry(name, dir), f.target)
		if got.Kind != wantKind {
			t.Errorf("%s: kind = %q, want %q", name, got.Kind, wantKind)
		}
		if wantRepo != "" && got.Repo != wantRepo {
			t.Errorf("%s: repo = %q, want %q", name, got.Repo, wantRepo)
		}
	}
	check("ce-plan", gdir, KindGit, "myrepo")
	check("my-local", ldir, KindLocal, "")
	check("copied", cdir, KindGit, "myrepo")
	check("find-skills", sdir, KindSkillsSh, "")
	check("plug", pdir, KindPlugin, "")
	check("hand-made", hdir, KindHandwritten, "")
	check("sig", sigdir, KindGit, "otherrepo")

	if got := c.Classify(entry("find-skills", sdir), f.target); got.SourceURL != "https://github.com/vercel-labs/skills.git" {
		t.Errorf("skills.sh sourceUrl = %q", got.SourceURL)
	}
}

func TestClassifyCaseInsensitiveOwnership(t *testing.T) {
	f := newClfix(t)
	src := mkdir(t, filepath.Join(f.repos, "r", "find-skills"))
	// On-disk entry differs in case from the manifest record.
	dir := filepath.Join(f.target, "Find-Skills")
	symlink(t, src, dir)
	f.man.Links = append(f.man.Links, config.LinkRecord{Name: "find-skills", Target: f.target, Source: src, LinkType: config.LinkSymlink})
	got := f.classifier(SkillLock{}).Classify(entry("Find-Skills", dir), f.target)
	if got.Kind != KindGit {
		t.Errorf("case-mismatched owned link must classify as git, got %q (would offer bogus 收编)", got.Kind)
	}
}

func TestClassifyEdgeCases(t *testing.T) {
	f := newClfix(t)
	c := f.classifier(SkillLock{})

	// symlink escaping all allowed roots → unknown, no file read
	escape := filepath.Join(f.target, "escape")
	symlink(t, filepath.Join(f.root, "outside-everything"), escape)
	if got := c.Classify(entry("escape", escape), f.target); got.Kind != KindUnknown {
		t.Errorf("escaping symlink must be unknown, got %q", got.Kind)
	}

	// broken symlink (dangling target) → unknown, not a crash
	broken := filepath.Join(f.target, "broken")
	symlink(t, filepath.Join(f.repos, "gone", "nowhere"), broken)
	_ = os.RemoveAll(filepath.Join(f.repos, "gone"))
	if got := c.Classify(entry("broken", broken), f.target); got.Kind != KindGit && got.Kind != KindUnknown {
		// A broken link still under repos resolves by path (git); the key property
		// is it does not panic and yields a defined kind.
		t.Errorf("broken link should yield a defined kind, got %q", got.Kind)
	}

	// under ~/.agents but NOT in lockfile → unknown (can't attribute firmly)
	asrc := mkdir(t, filepath.Join(f.agentsSkills, "mystery"))
	adir := filepath.Join(f.target, "mystery")
	symlink(t, asrc, adir)
	if got := c.Classify(entry("mystery", adir), f.target); got.Kind != KindUnknown {
		t.Errorf("under .agents w/o lockfile entry → unknown, got %q", got.Kind)
	}
}
