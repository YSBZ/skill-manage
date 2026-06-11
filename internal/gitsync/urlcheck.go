package gitsync

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// scpLike matches the scp-style git remote `user@host:path` (no scheme).
var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:.+$`)

// shellMeta are characters that have no business in a git remote URL and whose
// presence signals an injection attempt (e.g. `ext::sh -c …`, command
// substitution, globbing).
const shellMeta = " \t\r\n;&|$`<>(){}[]!*?\\\"'"

// allowedSchemes is the scheme allowlist for repo URLs (R24). http is excluded
// in favor of https; file and ext (git's arbitrary-command transports) are
// rejected by omission.
var allowedSchemes = map[string]bool{
	"https": true,
	"ssh":   true,
	"git":   true,
}

// ValidateRepoURL enforces the scheme allowlist and rejects shell-metacharacter
// and option-injection URLs before any clone (R24). It accepts https/ssh/git
// scheme URLs and scp-style git@host:path remotes; it rejects file://, ext::,
// bare local paths, leading-dash option injections, and anything containing
// shell metacharacters.
func ValidateRepoURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("empty repo URL")
	}
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("repo URL has surrounding whitespace: %q", raw)
	}
	if strings.HasPrefix(raw, "-") {
		// Would be interpreted as a git option, not a URL.
		return fmt.Errorf("repo URL must not start with '-': %q", raw)
	}
	if strings.ContainsAny(raw, shellMeta) {
		return fmt.Errorf("repo URL contains forbidden characters: %q", raw)
	}
	if scpLike.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable repo URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		return fmt.Errorf("repo URL has no scheme (bare paths are rejected): %q", raw)
	}
	if !allowedSchemes[scheme] {
		return fmt.Errorf("repo URL scheme %q not allowed (use https/ssh/git): %q", scheme, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("repo URL %q missing host", raw)
	}
	return nil
}
