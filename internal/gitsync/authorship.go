package gitsync

import (
	"context"
	"strings"

	"skillmanage/internal/scanner"
)

// SkillAuthor is the git-derived authorship of a skill path: who first added it
// (the creator) and who last touched it, each with a short date (YYYY-MM-DD).
// Fields are empty when history is unavailable (e.g. the path is untracked —
// staged but never committed).
type SkillAuthor struct {
	Creator    string `json:"creator"`
	CreatedAt  string `json:"createdAt"`
	LastAuthor string `json:"lastAuthor"`
	LastAt     string `json:"lastAt"`
}

// Authorship reads the creator (author of the first commit that ADDED relPath)
// and the last editor (most recent commit touching it) from git log. It is
// purely local (no fetch). A name and date are split on a unit-separator so a
// name containing spaces or '|' is handled correctly.
func (s *Syncer) Authorship(ctx context.Context, dir, relPath string) SkillAuthor {
	var a SkillAuthor
	first := func(args ...string) (string, string) {
		out, _, err := s.run(ctx, dir, args...)
		if err != nil {
			return "", ""
		}
		line := out
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return "", ""
		}
		if i := strings.IndexByte(line, '\x1f'); i >= 0 {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		return line, ""
	}

	// Creator: author of the commit that first added the path.
	a.Creator, a.CreatedAt = first("log", "--reverse", "--diff-filter=A", "--format=%an%x1f%ad", "--date=short", "--", relPath)
	if a.Creator == "" {
		// Fallback: earliest commit touching it (rename/copy can hide the add).
		a.Creator, a.CreatedAt = first("log", "--reverse", "--format=%an%x1f%ad", "--date=short", "--", relPath)
	}
	// Last editor: most recent commit touching the path.
	a.LastAuthor, a.LastAt = first("log", "-1", "--format=%an%x1f%ad", "--date=short", "--", relPath)
	return a
}

// RepoCreators returns each skill's creator (first-add author + date), keyed by
// the skill DIRECTORY NAME (the basename of its repo-relative path, e.g. "foo"
// for "skills/foo"), computed in a SINGLE git-log pass over the repo's add
// history — so the per-card creator badge costs one git call, not one per skill.
func (s *Syncer) RepoCreators(ctx context.Context, dir string) map[string]SkillAuthor {
	out := map[string]SkillAuthor{}
	skillRoot := scanner.SkillRoot(dir)
	o, _, err := s.run(ctx, dir, "log", "--reverse", "--diff-filter=A", "--name-only", "--date=short", "--format=%x00%an%x1f%ad")
	if err != nil {
		return out
	}
	var author, date string
	for _, line := range strings.Split(o, "\n") {
		if strings.HasPrefix(line, "\x00") { // commit header: \x00<author>\x1f<date>
			h := line[1:]
			if i := strings.IndexByte(h, '\x1f'); i >= 0 {
				author, date = strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:])
			} else {
				author, date = strings.TrimSpace(h), ""
			}
			continue
		}
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		sp := skillPathOf(p, skillRoot)
		if sp == "" {
			continue
		}
		name := lastSeg(sp)
		if _, seen := out[name]; !seen { // first add (oldest, since --reverse) wins
			out[name] = SkillAuthor{Creator: author, CreatedAt: date}
		}
	}
	return out
}
