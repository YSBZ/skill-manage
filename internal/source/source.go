// Package source classifies where a skill seen on disk originated, unifying git
// repos, the adopted/handwritten personal store, and read-only external
// directory sources (e.g. skills.sh's ~/.agents/skills) under one SourceKind
// taxonomy. SourceKind is derived at runtime, never persisted (phase 3 KTD1).
//
// This file holds the kind taxonomy and selector-level classification. The
// on-disk, per-entry classifier (ClassifyInTarget) lives in classify.go and
// reuses the linker's ownership predicate so "is this ours?" has exactly one
// implementation (KTD6).
package source

import "strings"

// SourceKind is the runtime-derived origin of a skill seen in a target dir. The
// values match the UI source badges (phase 3 U8): git, local, skills.sh,
// plugin, handwritten, unknown.
type SourceKind string

const (
	// KindGit is a skill from a tracked git repo source (canonical under
	// ~/.skillmanage/repos), owned and auto-updated by SkillManage.
	KindGit SourceKind = "git"
	// KindLocal is an adopted/hand-authored skill in the personal store
	// (canonical under ~/.skillmanage/local, the @local namespace).
	KindLocal SourceKind = "local"
	// KindSkillsSh is a skill installed by skills.sh (npx skills), recognized via
	// ~/.agents/.skill-lock.json. Read-only to SkillManage; updates go through
	// skills.sh's own tooling.
	KindSkillsSh SourceKind = "skills.sh"
	// KindPlugin is a skill living under an agent plugin tree (PluginRootFor).
	KindPlugin SourceKind = "plugin"
	// KindHandwritten is a real, unmanaged skill directory sitting in the target
	// (the "未备份 / 可收编" case) — not a link, and not owned by SkillManage.
	KindHandwritten SourceKind = "handwritten"
	// KindUnknown is a link SkillManage cannot attribute (foreign target, broken
	// symlink, symlink loop, or a path that escapes the allowed roots).
	KindUnknown SourceKind = "unknown"
)

// localNamespace mirrors reconcile.LocalNamespace ("@local"). It is duplicated
// here rather than imported to keep this leaf package free of a reconcile
// dependency (reconcile already depends on harness/linker/scanner/config).
const localNamespace = "@local"

// ClassifySelector classifies an enabled[] selector — "@local/<skill>" or
// "<repo>/<skill>" — by its namespace alone. It answers "git vs local" for a
// configured selection; the richer on-disk attribution (skills.sh / plugin /
// handwritten / unknown) requires filesystem inspection and lives in
// ClassifyInTarget. A selector with no "/" separator is KindUnknown.
func ClassifySelector(selector string) SourceKind {
	s := strings.TrimSpace(selector)
	i := strings.Index(s, "/")
	if i < 0 {
		return KindUnknown
	}
	if s[:i] == localNamespace {
		return KindLocal
	}
	return KindGit
}
