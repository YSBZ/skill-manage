package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Credential is one stored HTTPS credential for a git host. Token is a personal
// access token (PAT) — preferred over a raw password (scoped, revocable).
type Credential struct {
	Username string `yaml:"username"`
	Token    string `yaml:"token"`
}

// Credentials maps a git host (e.g. "github.com") to its HTTPS credential. It is
// stored plaintext in a 0600 file, machine-local, and never exported (KTD5):
// secrets must not travel with the portable repo list.
type Credentials struct {
	Hosts map[string]Credential `yaml:"hosts"`
}

// LoadCredentials reads the per-host credential file. A missing file is not an
// error — it yields an empty set.
func LoadCredentials(centralDir string) (Credentials, error) {
	var c Credentials
	b, err := os.ReadFile(CredentialsPath(centralDir))
	if os.IsNotExist(err) {
		c.Hosts = map[string]Credential{}
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read credentials: %w", err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse credentials: %w", err)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]Credential{}
	}
	return c, nil
}

// SaveCredentials writes the credential file with 0600 perms (owner-only),
// since it holds secrets.
func SaveCredentials(centralDir string, c Credentials) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.WriteFile(CredentialsPath(centralDir), b, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}
