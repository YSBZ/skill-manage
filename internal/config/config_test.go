package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Config{
		Repos: []RepoConfig{
			{URL: "git@example.com:backend-skills.git", Branch: "main"},
			{URL: "https://example.com/frontend-skills.git"},
		},
		Enabled: []EnabledEntry{
			{Skill: "backend-skills/*", Target: "~/.claude/skills/", Mode: ModeFollow},
			{Skill: "frontend-skills/foo", Target: "/proj/.claude/skills/", Mode: ModeSnapshot},
		},
		Projects: []string{"/proj"},
		Schedule: Schedule{DailyAt: "09:00"},
	}
	if err := SaveConfig(dir, want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, firstRun, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if firstRun {
		t.Fatal("firstRun should be false after a save")
	}
	if len(got.Repos) != 2 || got.Repos[0].Branch != "main" || got.Repos[1].URL != "https://example.com/frontend-skills.git" {
		t.Errorf("repos round-trip mismatch: %+v", got.Repos)
	}
	if len(got.Enabled) != 2 || got.Enabled[0].Mode != ModeFollow || got.Enabled[1].Mode != ModeSnapshot {
		t.Errorf("enabled round-trip mismatch: %+v", got.Enabled)
	}
	if got.Schedule.DailyAt != "09:00" {
		t.Errorf("schedule round-trip mismatch: %+v", got.Schedule)
	}
}

func TestLoadConfigMissingIsFirstRun(t *testing.T) {
	dir := t.TempDir()
	cfg, firstRun, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig on missing file should not error: %v", err)
	}
	if !firstRun {
		t.Fatal("missing config should report firstRun=true")
	}
	if cfg.Schedule.DailyAt != "09:00" {
		t.Errorf("missing config should yield DefaultConfig, got %+v", cfg)
	}
}

func TestLoadConfigMalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte("repos: [this is : not valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(dir); err == nil {
		t.Fatal("malformed YAML should return an error, not panic or succeed")
	}
}

func TestManifestRoundTripWithMissingSource(t *testing.T) {
	dir := t.TempDir()
	want := Manifest{Links: []LinkRecord{
		{Name: "ce-plan", Target: "~/.claude/skills/", Source: filepath.Join(dir, "gone", "ce-plan"), LinkType: LinkSymlink},
		{Name: "deploy", Target: "/proj/.claude/skills/", Source: "/repos/ops/deploy", LinkType: LinkJunction},
	}}
	if err := SaveManifest(dir, want); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got.Links) != 2 {
		t.Fatalf("manifest round-trip length mismatch: %+v", got.Links)
	}
	if got.Links[0].LinkType != LinkSymlink || got.Links[1].LinkType != LinkJunction {
		t.Errorf("link_type round-trip mismatch: %+v", got.Links)
	}
	// An entry whose source no longer exists must still round-trip — dangling
	// detection is the linker's job, not the manifest's.
	if got.Links[0].Source != want.Links[0].Source {
		t.Errorf("missing-source entry not preserved: %q", got.Links[0].Source)
	}
}

func TestLoadManifestMissingIsEmpty(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest on missing file should not error: %v", err)
	}
	if len(m.Links) != 0 {
		t.Errorf("missing manifest should be empty, got %+v", m.Links)
	}
}

func TestManifestFilePermsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := SaveManifest(dir, Manifest{}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	info, err := os.Stat(ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("manifest perm = %o, want 0600 (gates no-clobber safety)", perm)
	}
}
