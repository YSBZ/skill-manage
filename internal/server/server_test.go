package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/harness"
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

func TestAddRepoRejectsNameCollision(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/repos", s.token, repoReq{URL: "git@github.com:teamA/skills.git"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("first add: got %d, want 201", w.Code)
	}
	// distinct URL, same last segment → same on-disk dir → must be rejected
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/repos", s.token, repoReq{URL: "git@gitlab.com:teamB/skills.git"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("colliding add: got %d, want 409", w.Code)
	}
	cfg, _, _ := config.LoadConfig(s.centralDir)
	if len(cfg.Repos) != 1 {
		t.Errorf("collision must not be persisted, got %+v", cfg.Repos)
	}
}

func TestImportRejectsNameCollision(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	// two valid URLs whose names both derive to "skills" → batch-internal collision
	body := importReq{Repos: []repoReq{
		{URL: "git@github.com:teamA/skills.git"},
		{URL: "git@gitlab.com:teamB/skills.git"},
	}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/repos/import", s.token, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("import with name collision: got %d, want 400", w.Code)
	}
	cfg, _, _ := config.LoadConfig(s.centralDir)
	if len(cfg.Repos) != 0 {
		t.Errorf("no repos should be added when import collides, got %+v", cfg.Repos)
	}
}

func TestWaitForSyncsDrains(t *testing.T) {
	s := newTestServer(t)
	// Idle: WaitForSyncs must not deadlock.
	s.WaitForSyncs()
	// No repos configured → each SyncAll is a fast no-op. Running several keeps
	// the WaitGroup Add/Done balanced (an unbalanced Done would panic), and a
	// final WaitForSyncs returns cleanly. (The in-flight blocking case can't be
	// exercised deterministically without a blocking syncer seam; in production
	// the drained goroutines are launched at startup, long before shutdown.)
	for i := 0; i < 5; i++ {
		s.SyncAll(context.Background(), false)
	}
	s.WaitForSyncs()
}

func TestTargetsEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from real install dirs (discovery)
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	// add a cc and a codex directory explicitly (no reliance on discovery)
	for _, d := range []string{"~/.claude/skills/", "~/.codex/skills/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": d}))
		if w.Code != http.StatusCreated {
			t.Fatalf("add %s: got %d", d, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/targets", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("targets: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "~/.claude/skills/") || !strings.Contains(body, `"cc"`) ||
		!strings.Contains(body, "~/.codex/skills/") || !strings.Contains(body, `"codex"`) {
		t.Errorf("targets should list both dirs with cc/codex labels, got %s", body)
	}
	// missing token → 401
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/targets", "", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("targets without token: got %d, want 401", w.Code)
	}
}

func TestAddEnabledRejectsGuardedTarget(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/enabled", s.token, config.EnabledEntry{
		Skill: "demo/foo", Target: "~/.codex/skills/.system", Mode: config.ModeSnapshot,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("guarded target should be 400, got %d", w.Code)
	}
	cfg, _, _ := config.LoadConfig(s.centralDir)
	if len(cfg.Enabled) != 0 {
		t.Errorf("guarded enable must not persist, got %+v", cfg.Enabled)
	}
}

func TestAddRemoveTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from discovery → start with 0 targets
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	// add a new sync dir
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": "/work/proj/.claude/skills"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("add target: got %d, want 201", w.Code)
	}
	cfg, _, _ := config.LoadConfig(s.centralDir)
	if len(cfg.Targets) != 1 {
		t.Fatalf("target not persisted: %+v", cfg.Targets)
	}
	// guarded dir rejected
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": "~/.codex/skills/.system"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("guarded target add: got %d, want 400", w.Code)
	}
	// duplicate rejected
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": "/work/proj/.claude/skills"}))
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate target add: got %d, want 409", w.Code)
	}
	// same path with a trailing slash is the same directory → also rejected
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": "/work/proj/.claude/skills/"}))
	if w.Code != http.StatusConflict {
		t.Errorf("trailing-slash duplicate should be rejected, got %d", w.Code)
	}
	// remove it, and any enabled entry pointing at it
	s.mu.Lock()
	s.cfg.Enabled = append(s.cfg.Enabled, config.EnabledEntry{Skill: "r/foo", Target: "/work/proj/.claude/skills"})
	s.mu.Unlock()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("DELETE", "/api/targets", s.token, map[string]string{"dir": "/work/proj/.claude/skills"}))
	if w.Code != http.StatusOK {
		t.Fatalf("remove target: got %d, want 200", w.Code)
	}
	cfg, _, _ = config.LoadConfig(s.centralDir)
	if len(cfg.Targets) != 0 {
		t.Errorf("target not removed: %+v", cfg.Targets)
	}
	for _, e := range cfg.Enabled {
		if e.Target == "/work/proj/.claude/skills" {
			t.Errorf("enabled entry for removed target should be dropped: %+v", cfg.Enabled)
		}
	}
}

func TestRemoveTargetTearsDownLinks(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	// adopt a skill into a target so a managed symlink exists under that target
	dir := t.TempDir()
	skill := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: demo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		path string
		body map[string]string
	}{
		{"/api/targets", map[string]string{"dir": dir}},
		{"/api/adopt", map[string]string{"id": "demo", "root": dir}},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("POST", step.path, s.token, step.body))
		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("%s: %d body=%s", step.path, w.Code, w.Body.String())
		}
	}
	if fi, _ := os.Lstat(skill); fi == nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("precondition: %s should be a managed symlink after adopt", skill)
	}
	// removing the tab must tear that symlink off disk, not just drop config
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("DELETE", "/api/targets", s.token, map[string]string{"dir": dir}))
	if w.Code != http.StatusOK {
		t.Fatalf("remove target: %d", w.Code)
	}
	if _, err := os.Lstat(skill); !os.IsNotExist(err) {
		t.Errorf("symlink under removed target should be gone, lstat err = %v", err)
	}
}

func TestAdoptPluginImportsAndMaps(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	// Lay out a project: <proj>/.claude/skills (the tab) + a plugin skill under
	// the sibling <proj>/.claude/plugins/... tree.
	proj := t.TempDir()
	tab := filepath.Join(proj, ".claude", "skills")
	pskill := filepath.Join(proj, ".claude", "plugins", "cache", "p", "skills", "pdemo")
	for _, d := range []string{tab, pskill} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pskill, "SKILL.md"), []byte("---\nname: pdemo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	post := func(path string, body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("POST", path, s.token, body))
		return w
	}
	if w := post("/api/targets", map[string]string{"dir": tab}); w.Code != http.StatusCreated {
		t.Fatalf("add target: %d", w.Code)
	}
	// opt in to plugin skills
	if w := post("/api/ignore-plugins", map[string]bool{"ignore": false}); w.Code != http.StatusOK {
		t.Fatalf("opt-in: %d", w.Code)
	}
	// import the plugin skill into the tab
	w := post("/api/adopt", map[string]any{"id": "pdemo", "src": pskill, "target": tab, "plugin": true})
	if w.Code != http.StatusOK {
		t.Fatalf("plugin adopt: %d body=%s", w.Code, w.Body.String())
	}
	// store gets a COPY; the plugin original stays a real dir (not relocated)
	if _, err := os.Stat(filepath.Join(s.personalStore, "pdemo", "SKILL.md")); err != nil {
		t.Errorf("store copy missing: %v", err)
	}
	if fi, _ := os.Lstat(pskill); fi == nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("plugin original must stay a real dir, not be relocated/symlinked")
	}
	// the skill is now symlinked into the tab (reconcile ran)
	if fi, _ := os.Lstat(filepath.Join(tab, "pdemo")); fi == nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("plugin skill should be symlinked into the tab after import")
	}
	// guard: import is refused when not opted in
	if w := post("/api/ignore-plugins", map[string]bool{"ignore": true}); w.Code != http.StatusOK {
		t.Fatalf("opt-out: %d", w.Code)
	}
	if w := post("/api/adopt", map[string]any{"id": "pdemo2", "src": pskill, "target": tab, "plugin": true}); w.Code != http.StatusBadRequest {
		t.Errorf("plugin import should be refused when disabled, got %d", w.Code)
	}
}

func TestAdoptAddsEnabledMapping(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	// a real skill living in a directory we will register as a sync target
	dir := t.TempDir()
	skill := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: demo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/targets", s.token, map[string]string{"dir": dir}))
	if w.Code != http.StatusCreated {
		t.Fatalf("add target: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/adopt", s.token, map[string]string{"id": "demo", "root": dir}))
	if w.Code != http.StatusOK {
		t.Fatalf("adopt: %d body=%s", w.Code, w.Body.String())
	}
	// the in-place link must be recorded as a first-class mapping, otherwise
	// reconcile's orphan pass would delete it on the next sync.
	cfg, _, _ := config.LoadConfig(s.centralDir)
	found := false
	for _, e := range cfg.Enabled {
		if e.Skill == "@local/demo" && e.Target == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("adopt must add @local/demo → %s, got %+v", dir, cfg.Enabled)
	}
	if fi, _ := os.Lstat(skill); fi == nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("original should be a symlink into the store after adopt")
	}
}

func TestBackfillAdoptedEnabled(t *testing.T) {
	store := filepath.Join(t.TempDir(), "local")
	absClaude := harness.Expand("~/.claude/skills/")
	cfg := &config.Config{
		Targets: []string{"~/.claude/skills/"}, // configured (canonical) form
		// a previously-backfilled entry written in absolute form → must normalize
		Enabled: []config.EnabledEntry{{Skill: "@local/old", Target: absClaude, Mode: config.ModeSnapshot}},
	}
	m := &config.Manifest{Links: []config.LinkRecord{
		{Name: "old", Target: absClaude, Source: filepath.Join(store, "old"), LinkType: config.LinkSymlink},
		{Name: "foo", Target: absClaude, Source: filepath.Join(store, "foo"), LinkType: config.LinkSymlink},
		{Name: "bar", Target: "/x/.codex/skills", Source: "/other/bar"}, // not store-sourced → ignored
	}}
	if !backfillAdoptedEnabled(cfg, m, store) {
		t.Fatal("expected backfill to normalize + add the orphan store link")
	}
	bySkill := map[string]string{}
	for _, e := range cfg.Enabled {
		bySkill[e.Skill] = e.Target
	}
	// existing abs entry normalized to the configured form; new foo added with it
	if bySkill["@local/old"] != "~/.claude/skills/" {
		t.Errorf("existing entry should normalize to ~/.claude/skills/, got %q", bySkill["@local/old"])
	}
	if bySkill["@local/foo"] != "~/.claude/skills/" {
		t.Errorf("new entry should use the canonical target, got %q", bySkill["@local/foo"])
	}
	if len(cfg.Enabled) != 2 {
		t.Fatalf("want old + foo, got %+v", cfg.Enabled)
	}
	if backfillAdoptedEnabled(cfg, m, store) {
		t.Error("second run must be a no-op (idempotent)")
	}
}

func TestAdoptRejectsUnknownRoot(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	s := newTestServer(t)
	h := s.Handler()
	// an arbitrary root that is not one of the personal targets → 400, never an
	// attempt to relocate from a client-chosen path.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/adopt", s.token, map[string]string{"id": "foo", "root": t.TempDir()}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown adopt root should be 400, got %d", w.Code)
	}
	// missing root → 400 as well
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/api/adopt", s.token, map[string]string{"id": "foo"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing adopt root should be 400, got %d", w.Code)
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

func TestServerStartsWithoutGit(t *testing.T) {
	t.Setenv("PATH", "") // git not resolvable → must not crash startup
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New must succeed even without git, got %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/status", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "gitError") {
		t.Errorf("status should surface gitError when git is missing, got %s", w.Body.String())
	}
	// SyncAll must not panic with a nil syncer; it records the error instead.
	sum := s.SyncAll(context.Background(), false)
	_ = sum
}

func TestBrowseEndpoint(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// lists subdirectories only, with the resolved absolute path + a parent
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/browse?path="+root, s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("browse: got %d, want 200", w.Code)
	}
	var resp browseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != root {
		t.Errorf("path = %q, want %q", resp.Path, root)
	}
	if resp.Parent == "" {
		t.Errorf("parent should be non-empty for a tempdir")
	}
	if len(resp.Dirs) != 1 || resp.Dirs[0].Name != "skills" {
		t.Errorf("dirs should be just [skills], got %+v", resp.Dirs)
	}

	// a file path (not a dir) → 400
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/browse?path="+filepath.Join(root, "note.txt"), s.token, nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("browse on a file: got %d, want 400", w.Code)
	}

	// missing token → 401
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/api/browse?path="+root, "", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("browse without token: got %d, want 401", w.Code)
	}
}
