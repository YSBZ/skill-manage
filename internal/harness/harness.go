// Package harness resolves the set of agent skill directories SkillManage can
// link into ("targets"), classifies them by agent (harness) and scope, and
// guards directories the daemon must never write to. It is a leaf package with
// no SkillManage dependencies so reconcile/server can import it without cycles.
//
// Phase 2 turns SkillManage from a single-harness (Claude Code) linker into a
// cross-harness mapping layer: the same skill source can map into both Claude
// Code's and Codex's skill directories. Harness identity is carried by the
// target directory path (KTD1) — there is no persisted harness field — so this
// package centralizes the path↔harness knowledge that would otherwise scatter
// across the server and UI.
package harness

import (
	"os"
	"path/filepath"
	"strings"
)

// Harness identifies an agent that consumes skills.
type Harness string

const (
	HarnessClaudeCode Harness = "cc"
	HarnessCodex      Harness = "codex"
)

// Target is one linkable skill directory. Dir is the canonical string stored in
// EnabledEntry.Target and resolvable by reconcile.expandTarget (a leading "~"
// is kept for portability; an absolute path is used when an env override like
// CODEX_HOME forces one). There is no personal/project taxonomy — a target is
// just a directory the user chose. Harness (cc vs codex) is inferred from the
// path. Guarded directories are never returned as Targets — use Guarded() to
// test an arbitrary path on a write path.
type Target struct {
	Harness Harness `json:"harness"`
	Dir     string  `json:"dir"`
	Label   string  `json:"label"`
}

// Targets classifies each user-provided sync directory by the agent that
// consumes it — cc when the path is a Claude Code skills dir, codex when it is a
// .codex/.agents skills dir — and drops any blank or guarded entry. The order of
// dirs is preserved. cc and codex skills share one SKILL.md format, so the same
// source maps into either kind of target unchanged; the classification only
// drives the nested-SKILL.md guard (codex-only) and UI labeling.
func Targets(dirs []string) []Target {
	var out []Target
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" || Guarded(d) {
			continue
		}
		h := HarnessClaudeCode
		if IsCodexTarget(d) {
			h = HarnessCodex
		}
		out = append(out, Target{Harness: h, Dir: d, Label: string(h)})
	}
	return out
}

// Guarded reports whether dir is a Codex-owned directory the daemon must never
// link into, copy into, or adopt from: the bundled system-skill tree
// (<codexHome>/skills/.system) and the curated import clone
// (<codexHome>/vendor_imports/skills). Enforced on every write path (KTD7), not
// just hidden from target lists.
func Guarded(dir string) bool {
	r := expand(dir)
	system := filepath.Join(codexSkillsRoot(), ".system")
	vendor := filepath.Join(codexHome(), "vendor_imports", "skills")
	return underOrEqual(r, system) || underOrEqual(r, vendor)
}

// IsCodexTarget reports whether dir is a Codex skill directory (personal or
// project level). reconcile uses it to scope the nested-SKILL.md guard (KTD6)
// to Codex targets only.
func IsCodexTarget(dir string) bool {
	r := expand(dir)
	if underOrEqual(r, codexSkillsRoot()) {
		return true
	}
	return strings.HasSuffix(r, filepath.FromSlash("/.codex/skills")) ||
		strings.HasSuffix(r, filepath.FromSlash("/.agents/skills"))
}

// Expand resolves a leading "~" and returns a cleaned absolute path. It is the
// single canonical resolver for target Dirs (which keep the portable "~" form):
// callers outside this package — adopt's scan/relocate paths, the server's
// adopt-root validation — must resolve a Target.Dir through this, never through
// filepath.Abs (which does not expand "~").
func Expand(p string) string { return expand(p) }

// --- internal helpers ---

func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return expand(h)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return ".codex"
}

func codexSkillsRoot() string { return filepath.Join(codexHome(), "skills") }

// expand resolves a leading "~" and returns a cleaned absolute path. Mirrors
// reconcile.expandTarget intentionally — harness must not import reconcile.
func expand(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

// underOrEqual reports whether path is root or a descendant of root (both
// already cleaned/absolute), without being fooled by sibling prefixes like
// "/a/skills-x" vs "/a/skills".
func underOrEqual(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}
