package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillLock(t *testing.T) {
	dir := t.TempDir()

	// Missing file → empty, no error.
	l, err := LoadSkillLock(dir)
	if err != nil || len(l.Skills) != 0 {
		t.Fatalf("missing lock should be empty: %+v err=%v", l, err)
	}

	// v3 shape parses into our subset.
	content := `{
	  "version": 3,
	  "skills": {
	    "find-skills": {
	      "source": "vercel-labs/skills",
	      "sourceType": "github",
	      "sourceUrl": "https://github.com/vercel-labs/skills.git",
	      "skillFolderHash": "abc123",
	      "installedAt": "2026-06-15T10:23:54.438Z"
	    }
	  },
	  "lastSelectedAgents": ["claude-code", "codex"]
	}`
	if err := os.WriteFile(filepath.Join(dir, ".skill-lock.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err = LoadSkillLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := l.Has("find-skills")
	if !ok || e.SourceURL != "https://github.com/vercel-labs/skills.git" || e.Source != "vercel-labs/skills" || e.Hash != "abc123" {
		t.Errorf("lock entry mismatch: %+v ok=%v", e, ok)
	}
	if _, ok := l.Has("not-there"); ok {
		t.Error("Has should be false for unknown skill")
	}
}

func TestLoadSkillLockMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".skill-lock.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSkillLock(dir); err == nil {
		t.Fatal("malformed lock should error, not silently empty")
	}
}

func TestLoadSkillLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"skills":{"x":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A planted symlink at the lockfile path must not be followed.
	if err := os.Symlink(secret, filepath.Join(dir, ".skill-lock.json")); err != nil {
		t.Fatal(err)
	}
	l, err := LoadSkillLock(dir)
	if err != nil || len(l.Skills) != 0 {
		t.Fatalf("symlinked lockfile must be ignored, got %+v err=%v", l, err)
	}
}
