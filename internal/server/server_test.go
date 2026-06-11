package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skillmanage/internal/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func req(method, target, token string, body any) *http.Request {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Host = "localhost:7799"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestAuthRequired(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	// no token → 401
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/status", "", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", w.Code)
	}
	// wrong token → 401
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/status", "deadbeef", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", w.Code)
	}
	// correct token → 200
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/status", s.token, nil))
	if w.Code != http.StatusOK {
		t.Errorf("correct token: got %d, want 200", w.Code)
	}
}

func TestHostHeaderRejected(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r := req("GET", "/api/status", s.token, nil)
	r.Host = "evil.example.com" // DNS-rebinding attempt
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-loopback Host: got %d, want 403", w.Code)
	}
}

func TestSPAFallbackInjectsToken(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	// an arbitrary client-route path → index.html, no token required
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/some/spa/route", "", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("SPA route: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, tokenPlaceholder) {
		t.Error("token placeholder should have been replaced")
	}
	if !strings.Contains(body, s.token) {
		t.Error("served index.html should embed the real token")
	}
}

func TestAddRepoValidatesURL(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	// malicious URL → 400
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/repos", s.token, repoReq{URL: "ext::sh -c evil"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("malicious URL: got %d, want 400", w.Code)
	}

	// valid URL → 201, persisted
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/repos", s.token, repoReq{URL: "https://example.com/team/skills.git", Branch: "main"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("valid URL: got %d, want 201", w.Code)
	}
	cfg, _, err := config.LoadConfig(s.centralDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Branch != "main" {
		t.Errorf("repo not persisted: %+v", cfg.Repos)
	}
}

func TestImportRejectsAllOnInvalid(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	body := importReq{Repos: []repoReq{
		{URL: "https://example.com/a.git"},
		{URL: "file:///etc/passwd"}, // invalid → whole import rejected
	}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/repos/import", s.token, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("import with invalid entry: got %d, want 400", w.Code)
	}
	cfg, _, _ := config.LoadConfig(s.centralDir)
	if len(cfg.Repos) != 0 {
		t.Errorf("no repos should be added when import is rejected, got %+v", cfg.Repos)
	}
}

func TestBindFallback(t *testing.T) {
	s := newTestServer(t)
	// take an OS-assigned port
	ln1, err := s.Bind(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln1.Close()
	occupiedPort := ln1.Addr().(*net.TCPAddr).Port

	// request the now-occupied port → must fall back to a different free port
	ln2, err := s.Bind(occupiedPort)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if got := ln2.Addr().(*net.TCPAddr).Port; got == occupiedPort {
		t.Errorf("Bind should have fallen back to a different port, got same %d", got)
	}
}
