// Package source classifies where a skill seen on disk originated, unifying git
// repos, the adopted/handwritten personal store, and read-only external
// directory sources (e.g. skills.sh's ~/.agents/skills) under one SourceKind
// taxonomy. SourceKind is derived at runtime, never persisted (phase 3 KTD1).
//
// This file holds the kind taxonomy. The on-disk, per-entry classifier
// (ClassifyInTarget) lives in classify.go and reuses the linker's ownership
// predicate so "is this ours?" has exactly one implementation (KTD6).
package source

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
	// KindDir is a skill from a user-registered local directory source (a folder
	// the user added as a source). Canonical stays in that folder; SkillManage
	// links FROM it but never modifies it. Result.Repo carries the source id.
	KindDir SourceKind = "dir"
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

