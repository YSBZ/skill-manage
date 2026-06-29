package gitsync

import "testing"

func TestLooksSecret(t *testing.T) {
	secret := []string{
		"skills/foo/.env",
		"skills/foo/.env.local",
		"skills/foo/.env.production",
		".npmrc",
		"deploy/.netrc",
		"a/b/.git-credentials",
		"keys/id_rsa",
		"keys/id_ed25519",
		"certs/server.pem",
		"certs/private.key",
		"bundle.p12",
		"app.keystore",
		"credentials.yaml",
		"my.credentials.json",
		"secrets.json",
		"token.secret",
		".htpasswd",
	}
	for _, p := range secret {
		if !looksSecret(p) {
			t.Errorf("looksSecret(%q) = false, want true (should be flagged)", p)
		}
	}

	safe := []string{
		"skills/foo/SKILL.md",
		"skills/foo/.env.example",
		"skills/foo/.env.sample",
		"skills/foo/.env.template",
		"keys/id_rsa.pub",
		"README.md",
		"scripts/run.sh",
		"data/config.json",
		"notes.key.md", // ".md" suffix wins — not a bare *.key
		"",
	}
	for _, p := range safe {
		if looksSecret(p) {
			t.Errorf("looksSecret(%q) = true, want false (should NOT be flagged)", p)
		}
	}
}

func TestSecretsUnder(t *testing.T) {
	d := Drift{Secrets: []string{
		"skills/foo/.env",
		"skills/foo/keys/id_rsa",
		"skills/bar/cert.pem",
	}}
	if got := d.SecretsUnder("skills/foo"); len(got) != 2 {
		t.Errorf("SecretsUnder(skills/foo) = %v, want 2 entries", got)
	}
	if got := d.SecretsUnder("skills/bar"); len(got) != 1 {
		t.Errorf("SecretsUnder(skills/bar) = %v, want 1 entry", got)
	}
	if got := d.SecretsUnder("skills/baz"); len(got) != 0 {
		t.Errorf("SecretsUnder(skills/baz) = %v, want 0 entries", got)
	}
	// A path prefix that is not a directory boundary must not match.
	if got := d.SecretsUnder("skills/fo"); len(got) != 0 {
		t.Errorf("SecretsUnder(skills/fo) = %v, want 0 (prefix is not a dir boundary)", got)
	}
}
