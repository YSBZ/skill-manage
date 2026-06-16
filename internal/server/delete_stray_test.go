package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

func TestDeleteStrayLinkRemovesOnlySymlink(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	target := mkdirT(t, filepath.Join(base, "skills"))
	external := mkdirT(t, filepath.Join(base, "external", "stray"))
	os.WriteFile(filepath.Join(external, "SKILL.md"), []byte("---\nname: stray\n---\n"), 0o644)
	link := filepath.Join(target, "stray")
	symlinkT(t, external, link)
	s.mu.Lock(); s.cfg.Targets = []string{target}; s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteStrayLink(w, req("DELETE", "/api/inventory/link", s.token, map[string]string{"target": target, "name": "stray"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	// symlink gone…
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("stray symlink should be removed, err=%v", err)
	}
	// …but the target it pointed at is untouched.
	if _, err := os.Stat(external); err != nil {
		t.Errorf("symlink target must NOT be deleted, err=%v", err)
	}
}

func TestDeleteStrayLinkRefusesRealDir(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	real := mkdirT(t, filepath.Join(target, "hand-made"))
	os.WriteFile(filepath.Join(real, "SKILL.md"), []byte("---\nname: hand-made\n---\n"), 0o644)
	s.mu.Lock(); s.cfg.Targets = []string{target}; s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteStrayLink(w, req("DELETE", "/api/inventory/link", s.token, map[string]string{"target": target, "name": "hand-made"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("deleting a real dir must be refused, got %d", w.Code)
	}
	if _, err := os.Stat(real); err != nil {
		t.Errorf("real dir must survive, err=%v", err)
	}
}

func TestDeleteStrayLinkRefusesManagedLink(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	target := mkdirT(t, filepath.Join(base, "skills"))
	src := mkdirT(t, filepath.Join(s.reposRoot, "r", "owned"))
	link := filepath.Join(target, "owned")
	symlinkT(t, src, link)
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.manifest.Links = []config.LinkRecord{{Name: "owned", Target: target, Source: src, LinkType: config.LinkSymlink}}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleDeleteStrayLink(w, req("DELETE", "/api/inventory/link", s.token, map[string]string{"target": target, "name": "owned"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("deleting a managed link must be refused (use disable), got %d", w.Code)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("managed link must survive, err=%v", err)
	}
}

func TestDeleteStrayLinkRejectsTraversalAndBadTarget(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	s.mu.Lock(); s.cfg.Targets = []string{target}; s.mu.Unlock()
	for _, name := range []string{"../evil", "a/b", "..", ""} {
		w := httptest.NewRecorder()
		s.handleDeleteStrayLink(w, req("DELETE", "/api/inventory/link", s.token, map[string]string{"target": target, "name": name}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q must be rejected, got %d", name, w.Code)
		}
	}
	// unknown target
	w := httptest.NewRecorder()
	s.handleDeleteStrayLink(w, req("DELETE", "/api/inventory/link", s.token, map[string]string{"target": "/not/configured", "name": "x"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown target must be rejected, got %d", w.Code)
	}
	_ = url.QueryEscape
}
