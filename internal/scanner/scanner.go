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
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"skillmanage/internal/pathutil"
)

// Scan bounds, aligned with the Agent Skills client guidance (R5.2): cap the
// walk depth and total directory count so pointing Scan at a pathological tree
// (a huge monorepo, a symlinked cycle materialized as dirs) cannot hang. A real
// skill repo never approaches these — Scan also stops descending at each skill
// (KTD4) — so the caps only ever bite runaway inputs.
const (
	maxScanDepth = 6
	maxScanDirs  = 2000
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
	// Version is the SKILL.md frontmatter `version`, surfaced as a tag in the UI.
	// Empty when the skill declares no version.
	Version string `json:"version,omitempty"`
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
	// version may be authored as a string ("1.2.0") or a bare number (1.2 / 1) in
	// YAML, so it is decoded leniently and stringified by parseFrontmatter.
	Version any `yaml:"version"`
}

// parseFrontmatter extracts `description` and `version` from a SKILL.md
// frontmatter block (the YAML between a leading `---` line and the next `---`).
// Returns empty strings when there is no frontmatter or it cannot be parsed — a
// missing field is not an error.
func parseFrontmatter(skillMdPath string) (description, version string) {
	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		return "", ""
	}
	trimmed := bytes.TrimLeft(data, "\ufeff \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return "", ""
	}
	// drop the opening fence line, then split on the closing fence
	rest := trimmed[3:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return "", ""
	}
	var fm frontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil {
		return "", ""
	}
	return fm.Description, stringifyVersion(fm.Version)
}

// stringifyVersion renders a frontmatter version (string, int, or float) as a
// trimmed string; anything else (or absent) yields "".
func stringifyVersion(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// Scan walks repoRoot and returns its skill units, sorted by LinkName for
// determinism. A directory containing SKILL.md is a skill; scanning does not
// descend into a skill (the direct-child rule, KTD4) and skips .git.
func Scan(repoRoot string) ([]Skill, error) {
	var skills []Skill
	dirsSeen := 0
	rootSeps := strings.Count(filepath.Clean(repoRoot), string(os.PathSeparator))
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		// Skip dirs that never hold authored skills, per the standard's scan rules
		// (R5.2): VCS metadata and dependency caches.
		if name := d.Name(); name == ".git" || name == "node_modules" {
			return filepath.SkipDir
		}
		// Bound a runaway walk: stop entirely past the directory budget, and stop
		// descending past the depth cap. A real skill repo hits neither.
		dirsSeen++
		if dirsSeen > maxScanDirs {
			return filepath.SkipAll
		}
		if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootSeps > maxScanDepth {
			return filepath.SkipDir
		}
		info, statErr := os.Stat(filepath.Join(path, "SKILL.md"))
		if statErr == nil && !info.IsDir() {
			abs, absErr := filepath.Abs(path)
			if absErr != nil {
				return absErr
			}
			name := skillNameFromDir(path)
			desc, version := parseFrontmatter(filepath.Join(path, "SKILL.md"))
			skills = append(skills, Skill{
				LogicalName: name,
				LinkName:    pathutil.SanitizePathName(name),
				Dir:         abs,
				Description: desc,
				Version:     version,
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

// skillNameFromDir derives a skill's name from the directory holding its
// SKILL.md. When that directory is a generic wrapper ("skill"/"skills") — a
// layout some repos use, e.g. <realname>/skill/SKILL.md — the directory name is
// uninformative (and collides: every such skill would be "skill"), so the
// meaningful identity is the parent directory; use that instead. Falls back to
// the base name when the parent is missing or itself a generic wrapper.
func skillNameFromDir(path string) string {
	base := filepath.Base(path)
	if base == "skill" || base == "skills" {
		parent := filepath.Base(filepath.Dir(path))
		if parent != "" && parent != "." && parent != string(filepath.Separator) && parent != "skill" && parent != "skills" {
			return parent
		}
	}
	return base
}

// SkillRoot returns the repo-relative directory under which NEW skills should be
// created, inferred from where existing skills already live: the directory that
// holds the most skill folders. Many repos nest skills under a dedicated dir
// (e.g. "skills/<name>/SKILL.md") rather than at the repo root; a contribution
// must land alongside the rest, not at the root. Returns "" when the repo has no
// skills or they sit at the root. Slash-separated, no leading/trailing slash.
// Ties prefer a dedicated (non-root) directory so one stray root-level skill does
// not outvote a populated "skills/" dir.
func SkillRoot(repoDir string) string {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		abs = repoDir
	}
	skills, err := Scan(abs)
	if err != nil || len(skills) == 0 {
		return ""
	}
	counts := map[string]int{}
	order := []string{}
	for _, sk := range skills {
		rel, err := filepath.Rel(abs, filepath.Dir(sk.Dir))
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		if _, ok := counts[rel]; !ok {
			order = append(order, rel)
		}
		counts[rel]++
	}
	best, bestN := "", -1
	for _, rel := range order {
		n := counts[rel]
		if n > bestN || (n == bestN && best == "" && rel != "") {
			best, bestN = rel, n
		}
	}
	return best
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
		desc, version := parseFrontmatter(filepath.Join(dir, "SKILL.md"))
		skills = append(skills, Skill{
			LogicalName: name,
			LinkName:    pathutil.SanitizePathName(name),
			Dir:         abs,
			Description: desc,
			Version:     version,
			HasNested:   hasNestedSkillMd(dir),
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].LinkName < skills[j].LinkName })
	return skills, nil
}

// ScanInventory returns every direct child of root that holds a SKILL.md,
// INCLUDING symlinked children. ScanShallow deliberately skips symlinks (adoption
// only concerns real, unmanaged directories), but the inventory view (phase 3
// U6) must surface managed links and foreign tool links (skills.sh) too — it
// attributes each by source rather than only listing adoptable reals. Dir is the
// child's path AS IT SITS in root (the symlink path, not its resolved target) so
// the classifier can read the link. os.Stat follows the link to confirm it
// resolves to a directory containing SKILL.md.
func ScanInventory(root string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			continue
		}
		child := filepath.Join(root, name)
		fi, statErr := os.Stat(child) // follows symlink
		if statErr != nil || !fi.IsDir() {
			continue
		}
		md, mdErr := os.Stat(filepath.Join(child, "SKILL.md"))
		if mdErr != nil || md.IsDir() {
			continue
		}
		abs, absErr := filepath.Abs(child) // keeps the symlink path; does not resolve it
		if absErr != nil {
			return nil, absErr
		}
		desc, version := parseFrontmatter(filepath.Join(child, "SKILL.md"))
		skills = append(skills, Skill{
			LogicalName: name,
			LinkName:    pathutil.SanitizePathName(name),
			Dir:         abs,
			Description: desc,
			Version:     version,
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
