package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int // <0, 0, >0 (sign)
	}{
		{"5.1.9", "5.1.10", -1}, // numeric, not lexical (the bug a string compare hits)
		{"5.1.10", "5.1.9", 1},
		{"5.1.10", "5.1.10", 0},
		{"v5.1.10", "5.1.10", 0}, // leading v ignored
		{"5.2.0", "5.1.99", 1},
		{"5.1", "5.1.0", 0},  // missing segment treated as 0
		{"5.1.0", "5.1", 0},
		{"dev", "5.1.10", 1}, // non-numeric current sorts after → never falsely "newer"
	}
	for _, c := range cases {
		got := compareVersion(c.a, c.b)
		sign := func(n int) int {
			if n < 0 {
				return -1
			}
			if n > 0 {
				return 1
			}
			return 0
		}
		if sign(got) != c.want {
			t.Errorf("compareVersion(%q,%q)=%d, want sign %d", c.a, c.b, got, c.want)
		}
	}
	if !versionLess("5.1.9", "5.1.10") {
		t.Error("versionLess(5.1.9, 5.1.10) must be true")
	}
}

// TestUpdateCheckDisabled: with no feed built in (public build), the endpoint is
// inert — {enabled:false}, no network.
func TestUpdateCheckDisabled(t *testing.T) {
	UpdateFeed, UpdateToken = "", ""
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("GET", "/api/update-check", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if got := w.Body.String(); got == "" || !contains(got, `"enabled":false`) {
		t.Errorf("disabled build should report enabled:false, got %s", got)
	}
}

// TestUpdateCheckNewer: a fake feed returning a higher version → newer:true.
func TestUpdateCheckNewer(t *testing.T) {
	Version = "5.1.10"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"5.1.11","notes":"修了点东西","assets":{},"sha256":{}}`))
	}))
	defer ts.Close()
	UpdateFeed = ts.URL
	UpdateToken = ""
	defer func() { UpdateFeed, UpdateToken = "", "" }()

	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("GET", "/api/update-check", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if !contains(body, `"newer":true`) || !contains(body, `"latest":"5.1.11"`) {
		t.Errorf("expected newer:true latest 5.1.11, got %s", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
