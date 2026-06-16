package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockEntry is the subset of one skills.sh lock record we surface. Every field
// is third-party-written and UNTRUSTED — callers must validate before display
// (e.g. SourceURL must be checked for an http(s) scheme before rendering).
type LockEntry struct {
	Source    string // e.g. "vercel-labs/skills"
	SourceURL string // e.g. "https://github.com/vercel-labs/skills.git"
	Hash      string // skillFolderHash
}

// SkillLock is the parsed subset of skills.sh's <agents>/.skill-lock.json (v3).
type SkillLock struct {
	Skills map[string]LockEntry
}

// Has reports whether the lockfile tracks a skill by this name.
func (l SkillLock) Has(name string) (LockEntry, bool) {
	e, ok := l.Skills[name]
	return e, ok
}

// LoadSkillLock reads <agentsDir>/.skill-lock.json. A missing file is an empty
// lock (not an error) — many machines have skills.sh installed without a lock,
// or no skills.sh at all. The file is written by a third party (skills.sh) and
// is NOT trusted: we refuse to follow it if it is itself a symlink (planted
// redirect), parse only our known subset, and leave value-level trust to the
// caller. A present-but-malformed file is a real error so the UI can surface it
// rather than silently dropping recognitions.
func LoadSkillLock(agentsDir string) (SkillLock, error) {
	empty := SkillLock{Skills: map[string]LockEntry{}}
	if agentsDir == "" {
		return empty, nil
	}
	path := filepath.Join(agentsDir, ".skill-lock.json")
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return empty, nil // never follow a symlinked lockfile
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return SkillLock{}, fmt.Errorf("read skill-lock: %w", err)
	}
	var raw struct {
		Skills map[string]struct {
			Source    string `json:"source"`
			SourceURL string `json:"sourceUrl"`
			Hash      string `json:"skillFolderHash"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SkillLock{}, fmt.Errorf("parse skill-lock %s: %w", path, err)
	}
	out := SkillLock{Skills: make(map[string]LockEntry, len(raw.Skills))}
	for name, e := range raw.Skills {
		out.Skills[name] = LockEntry{Source: e.Source, SourceURL: e.SourceURL, Hash: e.Hash}
	}
	return out, nil
}
