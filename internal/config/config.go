// Package config defines SkillManage's on-disk configuration and ownership
// manifest, and resolves the central folder where both live.
//
// Two files live under the central folder (default ~/.skillmanage):
//   - config.yaml   — user intent: tracked repos, enabled skills, projects,
//     schedule. Portable; the repo list can be exported/imported (R2).
//   - manifest.yaml — ownership record of every link the daemon created.
//     Machine-local; NOT carried by export/import (KTD5). It is the safety
//     arbiter for "did we create this link?", always cross-checked against
//     the filesystem before any destructive operation.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Mode is the selection semantics for an enabled entry.
type Mode string

const (
	// ModeFollow ("全选") links every current skill in the repo and auto-links
	// upstream additions on the next sync (R9).
	ModeFollow Mode = "follow"
	// ModeSnapshot ("单选") links only the explicitly chosen skills (R10).
	ModeSnapshot Mode = "snapshot"
)

// LinkType records how a link was materialized on disk, so reconcile can treat
// copies as dirty-every-sync (KTD12) and detection can pick the right probe.
type LinkType string

const (
	LinkSymlink  LinkType = "symlink"
	LinkJunction LinkType = "junction"
	LinkCopy     LinkType = "copy"
)

// RepoConfig is one tracked git skill repo.
type RepoConfig struct {
	URL    string `yaml:"url" json:"url"`
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
}

// EnabledEntry maps a skill selection to a link target.
//
// Skill is either "<repo>/*" (follow the whole repo) or "<repo>/<skill-dir>"
// (a single skill). Target is one of the user's sync directories (config.Targets).
// Mode disambiguates follow vs snapshot for the "*" form; for a single skill it
// is implicitly snapshot.
type EnabledEntry struct {
	Skill  string `yaml:"skill" json:"skill"`
	Target string `yaml:"target" json:"target"`
	Mode   Mode   `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// Schedule controls the daily sync cadence.
type Schedule struct {
	// DailyAt is the local wall-clock time "HH:MM" the daily cycle fires.
	DailyAt string `yaml:"daily_at" json:"dailyAt"`
}

// Config is the user-facing, portable configuration (config.yaml).
//
// The user provides exactly two things: tracked repos (skill sources) and
// Targets (the directories to sync skills into). Targets are plain directories
// with no personal/project taxonomy — the consuming agent (cc vs codex) is
// inferred from each path by the harness package, not stored here.
type Config struct {
	Repos   []RepoConfig   `yaml:"repos"`
	Enabled []EnabledEntry `yaml:"enabled"`
	Targets []string       `yaml:"targets"`
	// TargetAliases is an optional display name per target dir (dir → alias),
	// shown on the tab instead of the raw path. Purely cosmetic.
	TargetAliases map[string]string `yaml:"target_aliases,omitempty"`
	// IncludePluginSkills, when true, lets skills living under a .../plugins/...
	// path appear in the adoptable ("未备份 skill") list. The zero value (false)
	// ignores them by default — they are managed by the agent's plugin system,
	// not hand-authored — which is what most users want.
	IncludePluginSkills bool     `yaml:"include_plugin_skills,omitempty"`
	Schedule            Schedule `yaml:"schedule"`
}

// LinkRecord is one daemon-owned link.
type LinkRecord struct {
	// Name is the (sanitized) link name as it appears under Target.
	Name string `yaml:"name" json:"name"`
	// Target is the directory the link lives in (skills root or project dir).
	Target string `yaml:"target" json:"target"`
	// Source is the absolute skill directory the link points at.
	Source string `yaml:"source" json:"source"`
	// LinkType is how the link was materialized.
	LinkType LinkType `yaml:"link_type" json:"linkType"`
}

// Manifest is the machine-local ownership record (manifest.yaml).
type Manifest struct {
	Links []LinkRecord `yaml:"links"`
}

const (
	configFileName   = "config.yaml"
	manifestFileName = "manifest.yaml"
)

// DefaultConfig is what a brand-new install starts from. Targets are NOT
// hardcoded here — the server discovers actually-present agent skill dirs at
// startup (harness.DiscoverDefaultTargets); anything not found is left for the
// user to add manually.
func DefaultConfig() Config {
	return Config{
		Schedule: Schedule{DailyAt: "09:00"},
	}
}

// ConfigPath returns the config.yaml path for a central folder.
func ConfigPath(centralDir string) string {
	return filepath.Join(centralDir, configFileName)
}

// ManifestPath returns the manifest.yaml path for a central folder.
func ManifestPath(centralDir string) string {
	return filepath.Join(centralDir, manifestFileName)
}

// PersonalStorePath returns the managed personal-skill store (`<central>/local`)
// where adopted local skills are relocated (U5). It is a second source root
// alongside the repos root; like repos it holds real skill directories the
// daemon links from. It follows centralDir rather than being hardcoded.
func PersonalStorePath(centralDir string) string {
	return filepath.Join(centralDir, "local")
}

// LoadConfig reads config.yaml from centralDir.
//
// When the file does not exist it returns DefaultConfig with firstRun=true so
// the caller can drive the first-run flow (R22) rather than treating absence as
// an error. A present-but-malformed file is a real error.
func LoadConfig(centralDir string) (cfg Config, firstRun bool, err error) {
	data, err := os.ReadFile(ConfigPath(centralDir))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), true, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse config %s: %w", ConfigPath(centralDir), err)
	}
	return cfg, false, nil
}

// SaveConfig writes config.yaml atomically (write temp, then rename).
func SaveConfig(centralDir string, cfg Config) error {
	if err := os.MkdirAll(centralDir, 0o755); err != nil {
		return fmt.Errorf("create central dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return writeFileAtomic(ConfigPath(centralDir), data, 0o644)
}

// LoadManifest reads manifest.yaml from centralDir. A missing manifest is an
// empty manifest, not an error — a fresh or migrated machine has none yet.
func LoadManifest(centralDir string) (Manifest, error) {
	data, err := os.ReadFile(ManifestPath(centralDir))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, nil
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest %s: %w", ManifestPath(centralDir), err)
	}
	return m, nil
}

// SaveManifest writes manifest.yaml atomically.
func SaveManifest(centralDir string, m Manifest) error {
	if err := os.MkdirAll(centralDir, 0o755); err != nil {
		return fmt.Errorf("create central dir: %w", err)
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	// 0600: the manifest gates the no-clobber safety model, so keep it
	// owner-only to reduce same-UID tampering surface.
	return writeFileAtomic(ManifestPath(centralDir), data, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp into place: %w", err)
	}
	return nil
}
