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
	// HarnessUnknown is a directory we cannot tie to a known agent by its path.
	// It is NOT defaulted to cc — the label stays "unknown" so the user is not
	// misled into thinking an arbitrary directory is a Claude Code dir.
	HarnessUnknown Harness = "unknown"
)

// Classify infers the consuming agent from a directory path: codex for
// .codex/.agents skill dirs (or under $CODEX_HOME/skills), cc for .claude skill
// dirs, otherwise unknown. cc is NOT the catch-all default.
func Classify(dir string) Harness {
	switch {
	case IsCodexTarget(dir):
		return HarnessCodex
	case isCCTarget(dir):
		return HarnessClaudeCode
	default:
		return HarnessUnknown
	}
}

// isCCTarget reports whether dir is a Claude Code skills directory: the personal
// ~/.claude/skills, or any */.claude/skills (project-level).
func isCCTarget(dir string) bool {
	r := expand(dir)
	if home, err := os.UserHomeDir(); err == nil {
		if underOrEqual(r, filepath.Join(home, ".claude", "skills")) {
			return true
		}
	}
	return strings.HasSuffix(r, filepath.FromSlash("/.claude/skills"))
}

// DiscoverDefaultTargets returns the conventional agent skill directories that
// actually exist on this machine, in canonical form. It probes the default
// install locations (Claude Code's ~/.claude/skills and Codex's
// $CODEX_HOME|~/.codex /skills) by existence — nothing is hardcoded into config,
// and a location that isn't present is simply left for the user to add manually.
func DiscoverDefaultTargets() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		if dirExists(filepath.Join(home, ".claude", "skills")) {
			out = append(out, "~/.claude/skills/")
		}
	}
	if dirExists(codexSkillsRoot()) {
		if os.Getenv("CODEX_HOME") != "" {
			out = append(out, codexSkillsRoot()) // abs form when overridden
		} else {
			out = append(out, "~/.codex/skills/")
		}
	}
	return out
}

// DiscoverDefaultDirectorySources returns the conventional cross-tool directory
// sources that actually exist on this machine, in canonical "~"-relative form.
// Currently that is the Agent Skills convention ~/.agents/skills — where
// skills.sh (npx skills) installs and other compliant tools share skills (phase
// 3 R1.4). These are recognized READ-ONLY: they are never link targets, so they
// are kept separate from DiscoverDefaultTargets. A location that isn't present
// is left for the user to register manually (R2.3); nothing is hardcoded into
// config. WSL needs no special handling — os.UserHomeDir() already returns the
// WSL distro's own home, so we only ever look at the current environment's home
// and never reach into a Windows host (/mnt/c).
func DiscoverDefaultDirectorySources() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		if dirExists(filepath.Join(home, ".agents", "skills")) {
			out = append(out, "~/.agents/skills/")
		}
	}
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

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
	Alias   string  `json:"alias,omitempty"` // optional user display name (cosmetic)
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
		out = append(out, Target{Harness: Classify(d), Dir: d})
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

// SkillDirsFor expands a user-selected directory into the concrete Claude Code
// and Codex skills directories it implies. Only these two agents are supported,
// so their well-known subdir conventions are matched directly — the user OK'd
// hardcoding the *pattern* (".claude/skills", ".codex/skills"), which is distinct
// from hardcoding absolute install paths into config:
//
//	dir itself          — when the selection already is a cc/codex skills dir
//	dir/skills          — when dir is a .claude/.codex home (e.g. ~/.claude)
//	dir/.claude/skills  — cc, for a project or home root (e.g. a repo checkout)
//	dir/.codex/skills   — codex
//	dir/.agents/skills  — codex (project-level alternative to .codex/skills)
//
// It returns the existing, non-guarded candidates — at most one per harness, so
// a project root yields exactly one cc + one codex target (.codex/skills wins
// over the .agents/skills alternative because it is probed first). Empty when
// none apply, so the caller can fall back to adding the selection verbatim.
func SkillDirsFor(dir string) []string {
	base := expand(dir)
	cands := []string{
		base,
		filepath.Join(base, "skills"),
		filepath.Join(base, ".claude", "skills"),
		filepath.Join(base, ".codex", "skills"),
		filepath.Join(base, ".agents", "skills"),
	}
	var out []string
	seenHarness := map[Harness]bool{}
	for _, c := range cands {
		h := Classify(c)
		if h == HarnessUnknown || seenHarness[h] || Guarded(c) || !dirExists(c) {
			continue
		}
		seenHarness[h] = true
		out = append(out, c)
	}
	return out
}

// PluginRootFor returns the agent plugin tree associated with a skills dir: the
// sibling "plugins" dir of the skills dir's parent (e.g. ~/.claude/skills →
// ~/.claude/plugins, /work/.codex/skills → /work/.codex/plugins). Plugin skills
// live nested under here, managed by the agent's plugin system rather than
// hand-authored, so the adopt scan only walks this tree when the user opts in.
func PluginRootFor(skillsDir string) string {
	d := expand(skillsDir)
	return filepath.Join(filepath.Dir(d), "plugins")
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
