package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ContribEntry is one skill's record in the contribution ledger (清单): the
// commit description last used for it and where it currently lives.
type ContribEntry struct {
	// Description is the commit summary source for this skill. It seeds from the
	// SKILL.md description on first backup/contribution and may be edited by the
	// user; subsequent moves reuse it (so the commit subject stays consistent).
	Description string `yaml:"description"`
	// Location is the skill's current home: "local" (the @local managed store) or
	// a git repo name (reconcile.RepoName). Updated on every move.
	Location string `yaml:"location"`
}

// ContribManifest is the global contribution ledger: skill name → entry. It is
// machine-local plaintext (0644), never exported and never pushed into a git
// repo — it is metadata about where skills went and what commit text to use.
type ContribManifest struct {
	Skills map[string]ContribEntry `yaml:"skills"`
}

// LoadContribManifest reads the ledger. A missing file is not an error — it
// yields an empty ledger.
func LoadContribManifest(centralDir string) (ContribManifest, error) {
	var m ContribManifest
	b, err := os.ReadFile(ContribManifestPath(centralDir))
	if os.IsNotExist(err) {
		m.Skills = map[string]ContribEntry{}
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("read contrib manifest: %w", err)
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("parse contrib manifest: %w", err)
	}
	if m.Skills == nil {
		m.Skills = map[string]ContribEntry{}
	}
	return m, nil
}

// SaveContribManifest writes the ledger (0644 — it holds no secrets).
func SaveContribManifest(centralDir string, m ContribManifest) error {
	b, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal contrib manifest: %w", err)
	}
	if err := os.WriteFile(ContribManifestPath(centralDir), b, 0o644); err != nil {
		return fmt.Errorf("write contrib manifest: %w", err)
	}
	return nil
}

// UpsertContrib loads the ledger, sets skill → {description, location}, and
// saves. A blank description preserves any existing one (a move should not wipe
// a previously-edited summary just because the caller passed none).
func UpsertContrib(centralDir, skill, description, location string) error {
	m, err := LoadContribManifest(centralDir)
	if err != nil {
		return err
	}
	e := m.Skills[skill]
	if description != "" {
		e.Description = description
	}
	e.Location = location
	m.Skills[skill] = e
	return SaveContribManifest(centralDir, m)
}
