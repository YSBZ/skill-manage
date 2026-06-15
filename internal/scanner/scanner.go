// Package scanner enumerates skill units in a repo: every directory containing
// a SKILL.md is one skill (R8), identified by its directory name and linked as
// a direct child of the skills root (KTD4).
package scanner

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"skillmanage/internal/pathutil"
)

// Skill is one discovered skill unit.
type Skill struct {
	// LogicalName is the source directory name, preserved verbatim.
	LogicalName string `json:"logicalName"`
	// LinkName is the filesystem-safe name used for the link (KTD3).
	LinkName string `json:"linkName"`
	// Dir is the absolute path to the skill directory.
	Dir string `json:"dir"`
	// Description is the SKILL.md frontmatter `description`, surfaced in the UI
	// so cards show what each skill does. Empty when absent or unparseable.
	Description string `json:"description"`
	// HasNested is true when the skill directory contains a SKILL.md in a
	// subdirectory (a "compound" skill). Codex recursively scans a linked skill
	// dir and registers nested SKILL.md as independent skills (#22275), so a
	// nested source mapped to a Codex target pollutes Codex's list. reconcile
	// uses this to raise a nested-conflict warning for Codex targets only (KTD6).
	HasNested bool `json:"hasNested,omitempty"`
}

// frontmatter is the subset of SKILL.md YAML frontmatter the scanner reads.
type frontmatter struct {
	Description string `yaml:"description"`
}

// parseDescription extracts the `description` from a SKILL.md frontmatter block
// (the YAML between a leading `---` line and the next `---`). Returns "" when
// there is no frontmatter or it cannot be parsed — a missing description is not
// an error.
func parseDescription(skillMdPath string) string {
	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		return ""
	}
	trimmed := bytes.TrimLeft(data, "\ufeff \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return ""
	}
	// drop the opening fence line, then split on the closing fence
	rest := trimmed[3:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return ""
	}
	var fm frontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil {
		return ""
	}
	return fm.Description
}

// Scan walks repoRoot and returns its skill units, sorted by LinkName for
// determinism. A directory containing SKILL.md is a skill; scanning does not
// descend into a skill (the direct-child rule, KTD4) and skips .git.
func Scan(repoRoot string) ([]Skill, error) {
	var skills []Skill
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, statErr := os.Stat(filepath.Join(path, "SKILL.md"))
		if statErr == nil && !info.IsDir() {
			abs, absErr := filepath.Abs(path)
			if absErr != nil {
				return absErr
			}
			name := filepath.Base(path)
			skills = append(skills, Skill{
				LogicalName: name,
				LinkName:    pathutil.SanitizePathName(name),
				Dir:         abs,
				Description: parseDescription(filepath.Join(path, "SKILL.md")),
				HasNested:   hasNestedSkillMd(path),
			})
			return filepath.SkipDir // do not descend into a skill (KTD4)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].LinkName < skills[j].LinkName })
	return skills, nil
}

// ScanShallow returns only the skills that are DIRECT children of root — a
// subdirectory holding a SKILL.md at its own top level. Unlike Scan it never
// descends, so pointing it at a broad directory (e.g. ~/.claude, which holds
// plugins/, projects/, …) surfaces nothing instead of dredging up every
// deeply-nested plugin skill. This matches the adopt containment rule, which
// only accepts skills sitting directly under the managed skills dir (KTD4/KTD7).
func ScanShallow(root string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".git" {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, statErr := os.Stat(filepath.Join(dir, "SKILL.md"))
		if statErr != nil || info.IsDir() {
			continue
		}
		abs, absErr := filepath.Abs(dir)
		if absErr != nil {
			return nil, absErr
		}
		name := e.Name()
		skills = append(skills, Skill{
			LogicalName: name,
			LinkName:    pathutil.SanitizePathName(name),
			Dir:         abs,
			Description: parseDescription(filepath.Join(dir, "SKILL.md")),
			HasNested:   hasNestedSkillMd(dir),
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].LinkName < skills[j].LinkName })
	return skills, nil
}

// hasNestedSkillMd reports whether skillDir contains a SKILL.md below its root
// (i.e. inside a subdirectory). The root SKILL.md itself does not count.
func hasNestedSkillMd(skillDir string) bool {
	found := false
	_ = filepath.WalkDir(skillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != skillDir && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "SKILL.md" && filepath.Dir(p) != skillDir {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
