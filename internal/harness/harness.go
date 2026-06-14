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
	HarnessClaudeCode Harness = "claude-code"
	HarnessCodex      Harness = "codex"
)

// Scope distinguishes a personal (user-wide) target from a project-local one.
type Scope string

const (
	ScopePersonal Scope = "personal"
	ScopeProject  Scope = "project"
)

// Target is one linkable skill directory. Dir is the canonical string stored in
// EnabledEntry.Target and resolvable by reconcile.expandTarget (a leading "~"
// is kept for portability; an absolute path is used when an env override like
// CODEX_HOME forces one). Guarded directories are never returned as Targets —
// use Guarded() to test an arbitrary path on a write path.
type Target struct {
	Harness   Harness `json:"harness"`
	Scope     Scope   `json:"scope"`
	Dir       string  `json:"dir"`
	Label     string  `json:"label"`
	Ambiguous bool    `json:"ambiguous,omitempty"` // Codex project path .codex/skills vs .agents/skills
}

// PersonalTargets returns the user-wide targets: Claude Code and Codex.
func PersonalTargets() []Target {
	return []Target{
		{Harness: HarnessClaudeCode, Scope: ScopePersonal, Dir: ccPersonalDir, Label: "Claude Code · 个人"},
		{Harness: HarnessCodex, Scope: ScopePersonal, Dir: codexPersonalDir(), Label: "Codex · 个人"},
	}
}

// ProjectTargets derives the per-harness project-level targets for one
// registered project path. Claude Code is always <proj>/.claude/skills. Codex
// is <proj>/.codex/skills (the openai/codex repo's own practice), falling back
// to <proj>/.agents/skills (the official docs path) only when that exists and
// .codex/skills does not. When both exist the choice is .codex/skills and the
// target is flagged Ambiguous so the UI can surface the chosen path.
func ProjectTargets(projectPath string) []Target {
	proj := strings.TrimRight(strings.TrimSpace(projectPath), "/")
	if proj == "" {
		return nil
	}
	codexDir, ambiguous := codexProjectDir(proj)
	return []Target{
		{Harness: HarnessClaudeCode, Scope: ScopeProject, Dir: proj + "/.claude/skills", Label: "Claude Code · " + filepath.Base(proj)},
		{Harness: HarnessCodex, Scope: ScopeProject, Dir: codexDir, Label: "Codex · " + filepath.Base(proj), Ambiguous: ambiguous},
	}
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

// --- internal helpers ---

// ccPersonalDir keeps the tilde form Phase 1 already stored so existing enabled
// entries and links continue to match (no churn on upgrade).
const ccPersonalDir = "~/.claude/skills/"

func codexPersonalDir() string {
	// With CODEX_HOME set the path is not under ~, so store the absolute form;
	// otherwise keep the portable tilde form.
	if os.Getenv("CODEX_HOME") != "" {
		return codexSkillsRoot()
	}
	return "~/.codex/skills/"
}

func codexProjectDir(proj string) (dir string, ambiguous bool) {
	codex := proj + "/.codex/skills"
	agents := proj + "/.agents/skills"
	codexExists := isDir(expand(codex))
	agentsExists := isDir(expand(agents))
	switch {
	case codexExists && agentsExists:
		return codex, true // both present → prefer repo practice, flag ambiguity
	case agentsExists && !codexExists:
		return agents, false
	default:
		return codex, false // neither (or only .codex) → default; linker MkdirAll's on link
	}
}

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

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
