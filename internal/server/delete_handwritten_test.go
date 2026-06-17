package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/reconcile"
)

func TestDeleteHandwrittenRemovesRealSkillDir(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	dir := mkdirT(t, filepath.Join(target, "hand"))
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: hand\n---\n"), 0o644)
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteHandwritten(w, req("DELETE", "/api/inventory/handwritten", s.token, map[string]string{"target": target, "name": "hand"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("handwritten dir should be removed, err=%v", err)
	}
}

func TestDeleteHandwrittenRefusesSymlink(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	target := mkdirT(t, filepath.Join(base, "skills"))
	external := mkdirT(t, filepath.Join(base, "external", "x"))
	os.WriteFile(filepath.Join(external, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644)
	link := filepath.Join(target, "x")
	symlinkT(t, external, link)
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteHandwritten(w, req("DELETE", "/api/inventory/handwritten", s.token, map[string]string{"target": target, "name": "x"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a symlink is not a handwritten real skill, must be refused, got %d", w.Code)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink must survive, err=%v", err)
	}
}

func TestDeleteHandwrittenRefusesNonSkillDir(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	dir := mkdirT(t, filepath.Join(target, "notaskill")) // no SKILL.md
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteHandwritten(w, req("DELETE", "/api/inventory/handwritten", s.token, map[string]string{"target": target, "name": "notaskill"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("dir without SKILL.md must be refused, got %d", w.Code)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("non-skill dir must survive, err=%v", err)
	}
}

func TestDeleteLocalSkillRemovesCanonicalCopy(t *testing.T) {
	s := newTestServer(t)
	skill := mkdirT(t, filepath.Join(s.personalStore, "mine"))
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: mine\n---\n"), 0o644)
	s.mu.Lock()
	s.cfg.Enabled = []config.EnabledEntry{{Skill: reconcile.LocalNamespace + "/mine", Target: "~/x"}}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteLocalSkill(w, req("DELETE", "/api/local-skill", s.token, map[string]string{"name": "mine"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Errorf("local canonical copy should be removed, err=%v", err)
	}
	// its selection should be dropped so reconcile no longer wants its links
	s.mu.Lock()
	for _, e := range s.cfg.Enabled {
		if e.Skill == reconcile.LocalNamespace+"/mine" {
			t.Errorf("selection for deleted local skill must be dropped")
		}
	}
	s.mu.Unlock()
}

func TestDeleteLocalSkillRejectsTraversal(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.handleDeleteLocalSkill(w, req("DELETE", "/api/local-skill", s.token, map[string]string{"name": "../evil"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("traversal name must be rejected, got %d", w.Code)
	}
}
