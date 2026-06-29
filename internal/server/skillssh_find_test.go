package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// realFindOutput mirrors actual `npx skills find react` output (ANSI already
// stripped here; the parser strips ANSI itself). Note the ":"-containing skill
// segment, which the pkg regex must accept.
const realFindOutput = `
Install with npx skills add <owner/repo@skill>

vercel-labs/agent-skills@vercel-react-best-practices 484.6K installs
└ https://skills.sh/vercel-labs/agent-skills/vercel-react-best-practices

google-labs-code/stitch-skills@react:components 49.1K installs
└ https://skills.sh/google-labs-code/stitch-skills/react:components

vercel-labs/json-render@react 2.6K installs
└ https://skills.sh/vercel-labs/json-render/react
`

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, w.Body.String())
	}
	return m
}

func TestSkillsShFindParses(t *testing.T) {
	s, fr := setupDirSource(t)
	fr.findStdout = realFindOutput
	w := httptest.NewRecorder()
	s.handleSkillsShFind(w, req("GET", "/api/skillssh/find?q=react", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeBody(t, w)
	if m["available"] != true {
		t.Fatalf("available=%v, want true", m["available"])
	}
	results, _ := m["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(results), results)
	}
	// Sorted by installs desc: 484.6K, 49.1K, 2.6K.
	first := results[0].(map[string]any)
	if first["pkg"] != "vercel-labs/agent-skills@vercel-react-best-practices" {
		t.Errorf("first pkg=%v", first["pkg"])
	}
	if first["installsRaw"] != "484.6K" {
		t.Errorf("first installsRaw=%v, want 484.6K", first["installsRaw"])
	}
	if int(first["installs"].(float64)) != 484600 {
		t.Errorf("first installs=%v, want 484600", first["installs"])
	}
	if first["url"] != "https://skills.sh/vercel-labs/agent-skills/vercel-react-best-practices" {
		t.Errorf("first url=%v", first["url"])
	}
	second := results[1].(map[string]any)
	if second["pkg"] != "google-labs-code/stitch-skills@react:components" {
		t.Errorf("second pkg=%v (the colon form must parse)", second["pkg"])
	}
	// Every parsed pkg must round-trip through the add validation regex, so a
	// found skill is always installable.
	for _, r := range results {
		pkg, _ := r.(map[string]any)["pkg"].(string)
		if !skillsPkgRe.MatchString(pkg) {
			t.Errorf("parsed pkg %q does not pass skillsPkgRe — would 400 on add", pkg)
		}
	}
}

func TestSkillsShFindInstallsUnits(t *testing.T) {
	cases := map[string]int{"484.6K": 484600, "144.8K": 144800, "1K": 1000, "1.2M": 1200000, "42": 42, "garbage": 0, "": 0}
	for raw, want := range cases {
		if got := parseInstalls(raw); got != want {
			t.Errorf("parseInstalls(%q)=%d, want %d", raw, got, want)
		}
	}
}

func TestSkillsShFindEmptyQueryNoNpx(t *testing.T) {
	s, fr := setupDirSource(t)
	w := httptest.NewRecorder()
	s.handleSkillsShFind(w, req("GET", "/api/skillssh/find?q=", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	m := decodeBody(t, w)
	if m["available"] != true {
		t.Errorf("available=%v, want true", m["available"])
	}
	if rs, _ := m["results"].([]any); len(rs) != 0 {
		t.Errorf("empty query should yield no results, got %d", len(rs))
	}
	if fr.calls != 0 {
		t.Errorf("empty query must not invoke npx, got %d calls", fr.calls)
	}
}

func TestSkillsShFindLeadingDashRejected(t *testing.T) {
	s, fr := setupDirSource(t)
	for _, bad := range []string{"-g", "--help", "-rf"} {
		w := httptest.NewRecorder()
		s.handleSkillsShFind(w, req("GET", "/api/skillssh/find?q="+bad, s.token, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: got %d, want 400", bad, w.Code)
		}
	}
	if fr.calls != 0 {
		t.Errorf("flag-like query must not invoke npx, got %d", fr.calls)
	}
}

func TestSkillsShFindNpxUnavailable(t *testing.T) {
	s, _ := setupDirSource(t)
	s.mu.Lock()
	s.npxPath = ""
	s.mu.Unlock()
	w := httptest.NewRecorder()
	s.handleSkillsShFind(w, req("GET", "/api/skillssh/find?q=react", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	if decodeBody(t, w)["available"] != false {
		t.Errorf("npx unavailable should report available:false")
	}
}

func TestSkillsShFindFormatDrift(t *testing.T) {
	s, fr := setupDirSource(t)
	// Valid line, then junk lines that must be skipped, then another valid line.
	fr.findStdout = "owner/repo@good 5K installs\n└ https://skills.sh/owner/repo/good\nsome unparseable banner noise\n???garbage\nfoo/bar@also 3 installs\n└ https://skills.sh/foo/bar/also\n"
	w := httptest.NewRecorder()
	s.handleSkillsShFind(w, req("GET", "/api/skillssh/find?q=x", s.token, nil))
	m := decodeBody(t, w)
	results, _ := m["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("format drift: got %d results, want 2 (bad lines skipped): %+v", len(results), results)
	}
}

func TestSkillsShFindHonestFailure(t *testing.T) {
	s, fr := setupDirSource(t)
	fr.findErr = http.ErrServerClosed // any non-nil error
	w := httptest.NewRecorder()
	s.handleSkillsShFind(w, req("GET", "/api/skillssh/find?q=react", s.token, nil))
	m := decodeBody(t, w)
	if _, ok := m["error"]; !ok {
		t.Errorf("runner error should surface an error field, got %+v", m)
	}
	if rs, _ := m["results"].([]any); len(rs) != 0 {
		t.Errorf("failure should not fabricate results")
	}
}

func TestSkillsShFindCrossOrigin(t *testing.T) {
	s, _ := setupDirSource(t)
	r := req("GET", "/api/skillssh/find?q=react", s.token, nil)
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	s.handleSkillsShFind(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: got %d, want 403", w.Code)
	}
}
