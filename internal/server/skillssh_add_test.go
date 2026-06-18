package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkillsShAddSuccess(t *testing.T) {
	s, fr := setupDirSource(t)
	// Real CLI success markers (verified): "✓ <skill> (copied)" + "Installed N skill".
	fr.addStdout = "◇  Installed 1 skill\n  ✓ find-skills (copied)\n    → ~/.agents/skills/find-skills\nDone!"
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "vercel-labs/skills@find-skills"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeBody(t, w)
	if m["ok"] != true || m["status"] != "installed" {
		t.Errorf("unexpected resp: %+v", m)
	}
	if fr.lastAddPkg != "vercel-labs/skills@find-skills" {
		t.Errorf("add got pkg %q", fr.lastAddPkg)
	}
}

func TestSkillsShAddHonestFailureExitZero(t *testing.T) {
	s, fr := setupDirSource(t)
	// CLI prints a failure but exits 0 (addErr stays nil) — must NOT be reported ok.
	fr.addStdout = "✘ Failed to add: package not found"
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "owner/repo@missing"}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	m := decodeBody(t, w)
	if m["ok"] != false || m["status"] != "failed" {
		t.Errorf("exit-0 failure must be reported as failed, got %+v", m)
	}
	if _, ok := m["error"]; !ok {
		t.Errorf("failed add should carry an error reason")
	}
}

func TestSkillsShAddNoMatchIsFailure(t *testing.T) {
	s, fr := setupDirSource(t)
	// Real CLI output for a missing package: no "Failed"/"✗", just "No matching…".
	// Absence of a success marker must classify as failed (KTD5), with the reason.
	fr.addStdout = "■  No matching skills found for: nonexistent-xyz\n●  Available skills:\n  - find-skills"
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "vercel-labs/skills@nonexistent-xyz"}))
	m := decodeBody(t, w)
	if m["ok"] != false || m["status"] != "failed" {
		t.Errorf("no-match must be failed, got %+v", m)
	}
	if r, _ := m["error"].(string); !strings.Contains(r, "No matching") {
		t.Errorf("error should carry the no-match reason, got %q", m["error"])
	}
}

func TestSkillsShAddRejectsInvalidPkg(t *testing.T) {
	s, fr := setupDirSource(t)
	for _, bad := range []string{
		"find-skills",          // no slash / no @
		"owner/repo",           // no @
		"-rf/evil@x",           // leading dash
		"../../etc@x",          // traversal
		"owner/repo@skill; rm", // shell metachar / space
		"owner /repo@skill",    // space
		"",                     // empty
	} {
		w := httptest.NewRecorder()
		s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": bad}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("pkg %q: got %d, want 400", bad, w.Code)
		}
	}
	if fr.calls != 0 {
		t.Errorf("invalid pkg must not invoke npx, got %d calls", fr.calls)
	}
}

func TestSkillsShAddAcceptsColonSkill(t *testing.T) {
	s, fr := setupDirSource(t)
	fr.addStdout = "✔ Installed"
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "google-labs-code/stitch-skills@react:components"}))
	if w.Code != http.StatusOK {
		t.Fatalf("colon-form pkg should be accepted, got %d body=%s", w.Code, w.Body.String())
	}
	if fr.lastAddPkg != "google-labs-code/stitch-skills@react:components" {
		t.Errorf("add got pkg %q", fr.lastAddPkg)
	}
}

func TestSkillsShAddNpxUnavailable(t *testing.T) {
	s, fr := setupDirSource(t)
	s.mu.Lock()
	s.npxPath = ""
	s.mu.Unlock()
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "owner/repo@skill"}))
	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("npx unavailable: got %d, want 412", w.Code)
	}
	if fr.calls != 0 {
		t.Errorf("runner must not run without npx, got %d", fr.calls)
	}
}

func TestSkillsShAddCrossOrigin(t *testing.T) {
	s, _ := setupDirSource(t)
	r := req("POST", "/api/skillssh/add", s.token, map[string]string{"pkg": "owner/repo@skill"})
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	s.handleSkillsShAdd(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: got %d, want 403", w.Code)
	}
}
