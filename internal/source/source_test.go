package source

import "testing"

func TestClassifySelector(t *testing.T) {
	cases := []struct {
		selector string
		want     SourceKind
	}{
		{"@local/my-skill", KindLocal},
		{"@local/*", KindLocal},
		{"backend-skills/ce-plan", KindGit},
		{"backend-skills/*", KindGit},
		{"  frontend/foo  ", KindGit}, // trimmed
		{"no-separator", KindUnknown},
		{"", KindUnknown},
		// A repo literally named "@local" cannot exist (ValidRepoName rejects a
		// leading '@'), so a leading "@local/" is unambiguously the local store.
		{"@localish/x", KindGit}, // not the reserved namespace
	}
	for _, c := range cases {
		if got := ClassifySelector(c.selector); got != c.want {
			t.Errorf("ClassifySelector(%q) = %q, want %q", c.selector, got, c.want)
		}
	}
}
