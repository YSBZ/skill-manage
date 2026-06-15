package adopt

import (
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/harness"
	"skillmanage/internal/linker"
)

func ccRoot(dir string) []harness.Target {
	return []harness.Target{{Harness: harness.HarnessClaudeCode, Dir: dir}}
}

type env struct {
	root  string
	cc    string // claude skills dir
	store string // personal store
	mgr   *linker.Manager
	man   *config.Manifest
}

func newEnv(t *testing.T) env {
	t.Helper()
	t.Setenv("CODEX_HOME", "") // keep guarded() deterministic
	root := t.TempDir()
	cc := filepath.Join(root, "claude", "skills")
	store := filepath.Join(root, "store")
	if err := os.MkdirAll(cc, 0o755); err != nil {
		t.Fatal(err)
	}
	return env{root: root, cc: cc, store: store, mgr: linker.NewManager(filepath.Join(root, "repos"), store), man: &config.Manifest{}}
}

// mkRealSkill creates a real skill dir under cc with SKILL.md + an extra file.
func (e env) mkRealSkill(t *testing.T, name string) {
	t.Helper()
	d := filepath.Join(e.cc, name)
	if err := os.MkdirAll(filepath.Join(d, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func isSymlink(t *testing.T, p string) bool {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("lstat %s: %v", p, err)
	}
	return fi.Mode()&os.ModeSymlink != 0
}

func TestListAdoptablePluginToggle(t *testing.T) {
	e := newEnv(t)
	// a skills dir that lives under a plugins/ tree, with one real skill
	pluginSkills := filepath.Join(e.root, "claude", "plugins", "cache", "p", "skills")
	if err := os.MkdirAll(filepath.Join(pluginSkills, "pskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginSkills, "pskill", "SKILL.md"), []byte("---\nname: pskill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := []harness.Target{{Harness: harness.HarnessClaudeCode, Dir: pluginSkills}}

	// ignored by default (includePlugins=false)
	if list, err := ListAdoptable(roots, e.man, false, e.store); err != nil || len(list) != 0 {
		t.Errorf("plugin skill should be ignored by default, got %+v (err %v)", list, err)
	}
	// shown when opted in
	if list, err := ListAdoptable(roots, e.man, true, e.store); err != nil || len(list) != 1 || list[0].ID != "pskill" {
		t.Errorf("plugin skill should appear when included, got %+v (err %v)", list, err)
	}
}

func TestListAdoptableExcludesSymlinksAndOwned(t *testing.T) {
	e := newEnv(t)
	e.mkRealSkill(t, "alpha") // adoptable
	e.mkRealSkill(t, "owned") // real but manifest-owned (copy fallback case)
	// a symlink skill (our normal repo link) — Scan skips symlinks entirely
	if err := os.Symlink(filepath.Join(e.root, "elsewhere"), filepath.Join(e.cc, "linked")); err != nil {
		t.Fatal(err)
	}
	e.man.Links = append(e.man.Links, config.LinkRecord{Name: "owned", Target: e.cc, Source: filepath.Join(e.store, "owned"), LinkType: config.LinkCopy})

	list, err := ListAdoptable(ccRoot(e.cc), e.man, false, e.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "alpha" {
		t.Fatalf("expected only 'alpha' adoptable, got %+v", list)
	}
}

// mkRealSkillIn creates a real skill dir (SKILL.md + extra file) under an
// arbitrary root, so multi-root scenarios can be exercised without ~/.codex.
func mkRealSkillIn(t *testing.T, root, name, body string) {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListAdoptableSpansMultipleRoots(t *testing.T) {
	e := newEnv(t)
	e.mkRealSkill(t, "alpha") // under CC
	codex := filepath.Join(e.root, "codex", "skills")
	mkRealSkillIn(t, codex, "beta", "")

	list, err := ListAdoptable([]harness.Target{
		{Harness: harness.HarnessClaudeCode, Dir: e.cc},
		{Harness: harness.HarnessCodex, Dir: codex},
	}, e.man, false, e.store)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // id → harness
	for _, a := range list {
		got[a.ID] = a.Harness
		if a.Root == "" {
			t.Errorf("adoptable %q missing Root", a.ID)
		}
	}
	if got["alpha"] != "cc" || got["beta"] != "codex" {
		t.Fatalf("expected alpha→cc and beta→codex, got %+v", list)
	}
}

// TestAdoptCrossRootNameCollisionRefuses guards the flat-store hazard: CC/foo and
// Codex/foo both map to store/foo. The second adopt must refuse rather than link
// Codex's foo to CC's already-stored content (data loss).
func TestAdoptCrossRootNameCollisionRefuses(t *testing.T) {
	e := newEnv(t)
	mkRealSkillIn(t, e.cc, "foo", "claude body")
	if err := Adopt("foo", e.cc, e.store, e.mgr, e.man); err != nil {
		t.Fatalf("first adopt: %v", err)
	}
	codex := filepath.Join(e.root, "codex", "skills")
	mkRealSkillIn(t, codex, "foo", "codex body DIFFERENT length") // differs in size

	err := Adopt("foo", codex, e.store, e.mgr, e.man)
	var ae *Error
	if !asErr(err, &ae) || ae.Code != "name_taken" {
		t.Fatalf("cross-root same-name should be name_taken, got %v", err)
	}
	// Codex original must be left untouched (still a real dir, not a symlink).
	if isSymlink(t, filepath.Join(codex, "foo")) {
		t.Errorf("refused adopt must not have replaced the Codex original")
	}
	if _, err := os.Stat(filepath.Join(codex, "foo", "SKILL.md")); err != nil {
		t.Errorf("Codex original content must survive a refused adopt: %v", err)
	}
}

func TestAdoptHappyPath(t *testing.T) {
	e := newEnv(t)
	e.mkRealSkill(t, "alpha")

	if err := Adopt("alpha", e.cc, e.store, e.mgr, e.man); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	// store has the real body, both files present
	if _, err := os.Stat(filepath.Join(e.store, "alpha", "SKILL.md")); err != nil {
		t.Errorf("store SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.store, "alpha", "scripts", "run.sh")); err != nil {
		t.Errorf("store nested file missing (incomplete copy!): %v", err)
	}
	// original is now a symlink → store
	if !isSymlink(t, filepath.Join(e.cc, "alpha")) {
		t.Errorf("original should be a symlink after adopt")
	}
	// readable through the symlink
	if _, err := os.Stat(filepath.Join(e.cc, "alpha", "SKILL.md")); err != nil {
		t.Errorf("skill not usable through symlink: %v", err)
	}
	// manifest records it
	if len(e.man.Links) != 1 || e.man.Links[0].Name != "alpha" {
		t.Errorf("manifest should record the adopt link, got %+v", e.man.Links)
	}
	// no temp leftover
	if _, err := os.Stat(filepath.Join(e.store, ".tmp-alpha")); !os.IsNotExist(err) {
		t.Errorf("temp dir should be gone")
	}
}

func TestAdoptIdempotent(t *testing.T) {
	e := newEnv(t)
	e.mkRealSkill(t, "alpha")
	if err := Adopt("alpha", e.cc, e.store, e.mgr, e.man); err != nil {
		t.Fatal(err)
	}
	// second adopt: source is now our owned symlink → no-op, no error, no dup
	if err := Adopt("alpha", e.cc, e.store, e.mgr, e.man); err != nil {
		t.Fatalf("re-adopt should be a no-op, got %v", err)
	}
	if len(e.man.Links) != 1 {
		t.Errorf("re-adopt must not duplicate manifest link, got %+v", e.man.Links)
	}
}

func TestAdoptStoreUnwritableLeavesOriginalIntact(t *testing.T) {
	e := newEnv(t)
	e.mkRealSkill(t, "alpha")
	// make the store path a FILE so MkdirAll fails → copy_failed before any
	// destructive step; original must be untouched.
	if err := os.WriteFile(e.store, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Adopt("alpha", e.cc, e.store, e.mgr, e.man)
	if err == nil {
		t.Fatal("expected failure when store is unwritable")
	}
	var ae *Error
	if !asErr(err, &ae) || ae.Code != "copy_failed" {
		t.Errorf("want copy_failed, got %v", err)
	}
	// original still a real dir with content
	if isSymlink(t, filepath.Join(e.cc, "alpha")) {
		t.Errorf("original must NOT have been replaced on failure")
	}
	if _, err := os.Stat(filepath.Join(e.cc, "alpha", "scripts", "run.sh")); err != nil {
		t.Errorf("original content must survive failure: %v", err)
	}
	if len(e.man.Links) != 0 {
		t.Errorf("no manifest mutation on failure")
	}
}

func TestAdoptRejectsInvalidAndGuarded(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"", ".", "..", "../evil", "a/b", "@local"} {
		if err := Adopt(bad, e.cc, e.store, e.mgr, e.man); err == nil {
			t.Errorf("Adopt(%q) should be rejected", bad)
		}
	}
	// guarded source dir (Codex .system) must be refused on the write path
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	guardedCC := filepath.Join(codexHome, "skills", ".system")
	if err := os.MkdirAll(filepath.Join(guardedCC, "imagegen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guardedCC, "imagegen", "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Adopt("imagegen", guardedCC, e.store, e.mgr, e.man)
	var ae *Error
	if !asErr(err, &ae) || ae.Code != "guarded" {
		t.Errorf("adopting from a guarded dir should be 'guarded', got %v", err)
	}
}

func TestAdoptReentryAfterRenameUsesExistingCopy(t *testing.T) {
	e := newEnv(t)
	e.mkRealSkill(t, "alpha")
	// simulate a crash after the store copy completed but before relink: the
	// store already has a complete copy, original is still a real dir.
	if err := os.MkdirAll(filepath.Join(e.store, "alpha", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linker.CopyTree(filepath.Join(e.cc, "alpha"), filepath.Join(e.store, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := Adopt("alpha", e.cc, e.store, e.mgr, e.man); err != nil {
		t.Fatalf("re-entry adopt should complete: %v", err)
	}
	if !isSymlink(t, filepath.Join(e.cc, "alpha")) {
		t.Errorf("re-entry should finish the relink")
	}
}

func TestVerifyTreeMatch(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyTreeMatch(src, dst); err != nil {
		t.Errorf("identical trees should match: %v", err)
	}
	// dst missing a file present in src → mismatch (the data-loss guard)
	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("more"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyTreeMatch(src, dst); err == nil {
		t.Errorf("missing file in dst must fail verification")
	}
}

func asErr(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
