// Package scanner enumerates skill units in a repo: every directory containing
// a SKILL.md is one skill (R8), identified by its directory name and linked as
// a direct child of the skills root (KTD4).
package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"skillmanage/internal/pathutil"
)

// Skill is one discovered skill unit.
type Skill struct {
	// LogicalName is the source directory name, preserved verbatim.
	LogicalName string
	// LinkName is the filesystem-safe name used for the link (KTD3).
	LinkName string
	// Dir is the absolute path to the skill directory.
	Dir string
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
