package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

// registerDirSource wires a registered local folder source (id "src") at folder
// into the server config and returns folder.
func registerDirSource(t *testing.T, s *Server, folder string) {
	t.Helper()
	s.cfg.LocalSources = append(s.cfg.LocalSources, config.DirectorySource{Path: folder, Label: "src", ID: "src"})
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return err == nil
}

// TestMoveStoreLocalToDir moves an @local skill into a registered folder source,
// asserting the body relocates and the enabled selector repoints @local → @dir.
func TestMoveStoreLocalToDir(t *testing.T) {
	s := newTestServer(t)
	writeHandwritten(t, s.personalStore, "movee", "from local")
	folder := t.TempDir()
	registerDirSource(t, s, folder)
	tgt := t.TempDir()
	s.cfg.Enabled = []config.EnabledEntry{{Skill: "@local/movee", Target: tgt, Mode: config.ModeSnapshot}}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move-local", s.token,
		map[string]string{"id": "movee", "from": "local", "to": "@dir:src"}))
	if w.Code != http.StatusOK {
		t.Fatalf("move @local→@dir: %d %s", w.Code, w.Body.String())
	}
	if !exists(t, filepath.Join(folder, "movee", "SKILL.md")) {
		t.Errorf("skill not relocated into the registered folder")
	}
	if exists(t, filepath.Join(s.personalStore, "movee")) {
		t.Errorf("skill not removed from @local store")
	}
	migrated := false
	for _, e := range s.cfg.Enabled {
		if e.Skill == "@local/movee" {
			t.Errorf("old @local selector still present: %+v", s.cfg.Enabled)
		}
		if e.Skill == "@dir:src/movee" {
			migrated = true
		}
	}
	if !migrated {
		t.Errorf("selector not repointed to @dir:src/movee: %+v", s.cfg.Enabled)
	}
	cm, _ := config.LoadContribManifest(s.centralDir)
	if cm.Skills["movee"].Location != "@dir:src" {
		t.Errorf("ledger location = %q, want @dir:src", cm.Skills["movee"].Location)
	}
}

// TestMoveStoreDirToLocal moves a skill out of a registered folder back into the
// @local store.
func TestMoveStoreDirToLocal(t *testing.T) {
	s := newTestServer(t)
	folder := t.TempDir()
	writeHandwritten(t, folder, "outee", "from dir")
	registerDirSource(t, s, folder)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move-local", s.token,
		map[string]string{"id": "outee", "from": "@dir:src", "to": "local"}))
	if w.Code != http.StatusOK {
		t.Fatalf("move @dir→@local: %d %s", w.Code, w.Body.String())
	}
	if !exists(t, filepath.Join(s.personalStore, "outee", "SKILL.md")) {
		t.Errorf("skill not relocated into @local store")
	}
	if exists(t, filepath.Join(folder, "outee")) {
		t.Errorf("skill not removed from the registered folder")
	}
}

// TestMoveStoreDirToGit moves a skill out of a registered folder into a git repo
// (staged, not pushed).
func TestMoveStoreDirToGit(t *testing.T) {
	skipNoGit(t)
	s := newTestServer(t)
	if s.syncer == nil {
		t.Skip("no syncer")
	}
	if err := os.MkdirAll(s.reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := bareRemoteWithSeed(t, "repo-a")
	nameA, _ := cloneMirrorWithSkill(t, s.reposRoot, bare, "")
	s.cfg.Repos = []config.RepoConfig{{URL: bare}}
	folder := t.TempDir()
	writeHandwritten(t, folder, "contribee", "from dir")
	registerDirSource(t, s, folder)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move-local", s.token,
		map[string]string{"id": "contribee", "from": "@dir:src", "to": nameA}))
	if w.Code != http.StatusOK {
		t.Fatalf("move @dir→git: %d %s", w.Code, w.Body.String())
	}
	if !exists(t, filepath.Join(s.reposRoot, nameA, "contribee", "SKILL.md")) {
		t.Errorf("skill not staged in the dest repo working tree")
	}
	if exists(t, filepath.Join(folder, "contribee")) {
		t.Errorf("skill not removed from the registered folder")
	}
	if remoteHasSkill(t, bare, "contribee") {
		t.Errorf("dest upstream should not have the skill yet (move stages, no push)")
	}
}

// TestMoveGitToDir moves a git-repo skill into a registered folder source.
func TestMoveGitToDir(t *testing.T) {
	skipNoGit(t)
	s := newTestServer(t)
	if s.syncer == nil {
		t.Skip("no syncer")
	}
	if err := os.MkdirAll(s.reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := bareRemoteWithSeed(t, "repo-a")
	nameA, _ := cloneMirrorWithSkill(t, s.reposRoot, bare, "mover")
	s.cfg.Repos = []config.RepoConfig{{URL: bare}}
	folder := t.TempDir()
	registerDirSource(t, s, folder)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move", s.token,
		map[string]string{"name": "mover", "fromRepo": nameA, "toRepo": "@dir:src"}))
	if w.Code != http.StatusOK {
		t.Fatalf("move git→@dir: %d %s", w.Code, w.Body.String())
	}
	if !exists(t, filepath.Join(folder, "mover", "SKILL.md")) {
		t.Errorf("skill not copied into the registered folder")
	}
	// git source removal is a staged deletion in the working tree.
	if exists(t, filepath.Join(s.reposRoot, nameA, "mover")) {
		t.Errorf("skill not removed from the source repo working tree")
	}
}

// TestMoveStoreRejectsSameTarget refuses moving a folder skill onto itself.
func TestMoveStoreRejectsSameTarget(t *testing.T) {
	s := newTestServer(t)
	folder := t.TempDir()
	writeHandwritten(t, folder, "x", "d")
	registerDirSource(t, s, folder)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move-local", s.token,
		map[string]string{"id": "x", "from": "@dir:src", "to": "@dir:src"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("same source/target: got %d, want 400 (%s)", w.Code, w.Body.String())
	}
}

// TestMoveStoreSingleSkillFolderGuard refuses moving OUT a skill that IS the
// registered folder root itself (that would empty the user's source folder).
func TestMoveStoreSingleSkillFolderGuard(t *testing.T) {
	s := newTestServer(t)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("---\nname: solo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registerDirSource(t, s, folder)
	id := filepath.Base(folder) // a root-SKILL.md folder's skill LogicalName is the dir base
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move-local", s.token,
		map[string]string{"id": id, "from": "@dir:src", "to": "local"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("single-skill folder source should be rejected: got %d, want 400 (%s)", w.Code, w.Body.String())
	}
	if !exists(t, filepath.Join(folder, "SKILL.md")) {
		t.Errorf("guard must leave the source folder intact")
	}
}

// TestAddLocalSourceStoresAbsolutePath asserts a registered folder is persisted
// with its expanded absolute path, not the raw typed string.
func TestAddLocalSourceStoresAbsolutePath(t *testing.T) {
	s := newTestServer(t)
	folder := t.TempDir()
	writeHandwritten(t, folder, "demo", "d")
	// Pass a relative path (folder name) from the folder's parent so the stored
	// form must be absolutized to remain valid.
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/local-source", s.token,
		map[string]string{"dir": folder, "label": "src"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("add local source: %d %s", w.Code, w.Body.String())
	}
	cfg, _, _ := config.LoadConfig(s.centralDir)
	if len(cfg.LocalSources) != 1 {
		t.Fatalf("want 1 local source, got %+v", cfg.LocalSources)
	}
	got := cfg.LocalSources[0].Path
	if !filepath.IsAbs(got) {
		t.Errorf("stored path %q is not absolute", got)
	}
}
