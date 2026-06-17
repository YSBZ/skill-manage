package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
)

type fakeRunner struct {
	calls  int
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) UpdateSkill(ctx context.Context, npxPath, name string) (string, string, error) {
	f.calls++
	return f.stdout, f.stderr, f.err
}

func (f *fakeRunner) UpdateAll(ctx context.Context, npxPath string) (string, string, error) {
	f.calls++
	return f.stdout, f.stderr, f.err
}

func (f *fakeRunner) UpdatePlugin(ctx context.Context, cliPath, plugin, scope string) (string, string, error) {
	f.calls++
	return f.stdout, f.stderr, f.err
}

func (f *fakeRunner) ListPlugins(ctx context.Context, cliPath string) (string, string, error) {
	return f.stdout, f.stderr, f.err
}

func (f *fakeRunner) ListMarketplaces(ctx context.Context, cliPath string) (string, string, error) {
	return f.stdout, f.stderr, f.err
}

// setupDirSource wires a server with a fake npx + a ~/.agents/skills holding one
// real skill dir named "find-skills".
func setupDirSource(t *testing.T) (*Server, *fakeRunner) {
	t.Helper()
	s := newTestServer(t)
	base := t.TempDir()
	t.Setenv("HOME", base)
	agentsSkills := filepath.Join(base, ".agents", "skills")
	if err := os.MkdirAll(filepath.Join(agentsSkills, "find-skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{stdout: "updated find-skills"}
	s.mu.Lock()
	s.npxPath = "/fake/npx"
	s.runner = fr
	s.cfg.DirectorySources = []config.DirectorySource{{Path: agentsSkills}}
	s.mu.Unlock()
	return s, fr
}

func TestDirSourceUpdateRejectsMaliciousNames(t *testing.T) {
	s, fr := setupDirSource(t)
	for _, bad := range []string{"--version", "; rm -rf ~", "a/b", "..", "", "../evil", "-rf", "a b"} {
		w := httptest.NewRecorder()
		s.handleDirSourceUpdate(w, req("POST", "/api/dirsource/update", s.token, map[string]string{"name": bad}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q: got %d, want 400", bad, w.Code)
		}
	}
	if fr.calls != 0 {
		t.Errorf("runner must not be called for invalid names, got %d calls", fr.calls)
	}
}

func TestDirSourceUpdateRejectsNameNotOnDisk(t *testing.T) {
	s, fr := setupDirSource(t)
	w := httptest.NewRecorder()
	// passes the regex but has no matching dir under ~/.agents/skills
	s.handleDirSourceUpdate(w, req("POST", "/api/dirsource/update", s.token, map[string]string{"name": "not-installed"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
	if fr.calls != 0 {
		t.Errorf("runner must not run for a name absent on disk, got %d", fr.calls)
	}
}

func TestDirSourceUpdateNpxUnavailable(t *testing.T) {
	s, fr := setupDirSource(t)
	s.mu.Lock()
	s.npxPath = ""
	s.mu.Unlock()
	w := httptest.NewRecorder()
	s.handleDirSourceUpdate(w, req("POST", "/api/dirsource/update", s.token, map[string]string{"name": "find-skills"}))
	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("npx unavailable: got %d, want 412", w.Code)
	}
	if fr.calls != 0 {
		t.Errorf("runner must not run without npx, got %d", fr.calls)
	}
}

func TestDirSourceUpdateRejectsCrossOrigin(t *testing.T) {
	s, _ := setupDirSource(t)
	r := req("POST", "/api/dirsource/update", s.token, map[string]string{"name": "find-skills"})
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	s.handleDirSourceUpdate(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: got %d, want 403", w.Code)
	}
}

func TestDirSourceUpdateSuccess(t *testing.T) {
	s, fr := setupDirSource(t)
	w := httptest.NewRecorder()
	s.handleDirSourceUpdate(w, req("POST", "/api/dirsource/update", s.token, map[string]string{"name": "find-skills"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ok"] != true || resp["stdout"] != "updated find-skills" {
		t.Errorf("unexpected resp: %+v", resp)
	}
	if fr.calls != 1 {
		t.Errorf("runner calls = %d, want 1", fr.calls)
	}
}

func TestDirSourceUpdateRunnerError(t *testing.T) {
	s, fr := setupDirSource(t)
	fr.err = errors.New("exit status 1")
	fr.stderr = "boom"
	w := httptest.NewRecorder()
	s.handleDirSourceUpdate(w, req("POST", "/api/dirsource/update", s.token, map[string]string{"name": "find-skills"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ok"] != false || resp["stderr"] != "boom" || resp["error"] == nil {
		t.Errorf("error resp should surface stderr+error: %+v", resp)
	}
}
