package gitsync

import "testing"

func TestValidateRepoURL(t *testing.T) {
	allow := []string{
		"https://example.com/team/skills.git",
		"ssh://git@example.com/team/skills.git",
		"git://example.com/team/skills.git",
		"git@github.com:team/skills.git",
		"git@gitlab.internal:group/sub/skills.git",
	}
	for _, u := range allow {
		if err := ValidateRepoURL(u); err != nil {
			t.Errorf("ValidateRepoURL(%q) = %v, want allow", u, err)
		}
	}

	deny := map[string]string{
		"file:///etc/passwd":                 "file scheme",
		"ext::sh -c 'rm -rf /'":              "ext transport / metachars",
		"/tmp/local/repo":                    "bare local path",
		"-oProxyCommand=evil":                "option injection",
		"https://example.com/a b/skills.git": "whitespace metachar",
		"https://exa`mple.com/x.git":         "backtick metachar",
		"http://example.com/x.git":           "http not allowlisted",
		"":                                   "empty",
		"https://":                           "missing host",
	}
	for u, why := range deny {
		if err := ValidateRepoURL(u); err == nil {
			t.Errorf("ValidateRepoURL(%q) = nil, want reject (%s)", u, why)
		}
	}
}
