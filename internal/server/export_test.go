package server

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

// zipHasEntry reports whether the zip at path contains an entry with the given
// name.
func zipHasEntry(t *testing.T, path, entry string) bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == entry {
			return true
		}
	}
	return false
}

func decodeExport(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode export resp: %v (%s)", err, w.Body.String())
	}
	return m
}

// TestExportFromDirSource exports a skill that lives in a registered local folder
// (a real directory) and checks the zip lands under ~/.skillmanage/exports with a
// single top-level skill folder.
func TestExportFromDirSource(t *testing.T) {
	s := newTestServer(t)
	folder := t.TempDir()
	writeHandwritten(t, folder, "alpha", "export me")
	s.cfg.LocalSources = []config.DirectorySource{{Path: folder, Label: "src", ID: "src"}}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/export", s.token,
		map[string]string{"repo": "@dir:src", "name": "alpha"}))
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	m := decodeExport(t, w)
	if filepath.Dir(m["path"]) != filepath.Join(s.centralDir, "exports") {
		t.Errorf("zip saved outside exports dir: %q", m["path"])
	}
	if _, err := os.Stat(m["path"]); err != nil {
		t.Fatalf("zip not written: %v", err)
	}
	if !zipHasEntry(t, m["path"], "alpha/SKILL.md") {
		t.Errorf("zip missing alpha/SKILL.md")
	}
}

// TestExportFromProjectSymlink exports a project-side skill that is a SYMLINK into
// a source dir — the link must be resolved so the zip carries the real files.
func TestExportFromProjectSymlink(t *testing.T) {
	src := t.TempDir()
	writeHandwritten(t, src, "beta", "real body")
	target := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "beta"), filepath.Join(target, "beta")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	s := newTestServer(t)
	s.cfg.Targets = []string{target}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/export", s.token,
		map[string]string{"target": target, "name": "beta"}))
	if w.Code != http.StatusOK {
		t.Fatalf("export project symlink: %d %s", w.Code, w.Body.String())
	}
	m := decodeExport(t, w)
	if !zipHasEntry(t, m["path"], "beta/SKILL.md") {
		t.Errorf("zip missing beta/SKILL.md (symlink not resolved?)")
	}
}

// TestExportUnknownSkill returns 404 for a missing skill.
func TestExportUnknownSkill(t *testing.T) {
	s := newTestServer(t)
	folder := t.TempDir()
	writeHandwritten(t, folder, "alpha", "x")
	s.cfg.LocalSources = []config.DirectorySource{{Path: folder, Label: "src", ID: "src"}}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/export", s.token,
		map[string]string{"repo": "@dir:src", "name": "nope"}))
	if w.Code != http.StatusNotFound {
		t.Errorf("missing skill: got %d, want 404", w.Code)
	}
}
