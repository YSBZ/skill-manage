// Package linker materializes and removes skill links cross-platform, with an
// ownership manifest that is always cross-checked against the filesystem before
// any destructive operation (KTD5). It never clobbers a real directory (R14),
// detects within-root collisions and cross-target shadowing (R13 + shadowing),
// and prunes dangling links (R15, KTD10).
//
// Platform primitives (create, link-type probe) live in linker_unix.go and
// linker_windows.go behind build tags; this file holds the platform-independent
// orchestration.
package linker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"skillmanage/internal/config"
	"skillmanage/internal/harness"
)

var (
	// ErrTargetOccupied means a real directory (or a foreign link the daemon
	// did not create) sits at the target — the tool stops rather than clobber
	// it (R14).
	ErrTargetOccupied = errors.New("target occupied by a non-owned path")
	// ErrDiverged means the manifest claims ownership but the on-disk path is
	// no longer the link we recorded (e.g. replaced by a real dir). The tool
	// refuses to treat it as owned (KTD5).
	ErrDiverged = errors.New("manifest entry diverged from filesystem")
)

// DesiredLink is a link the reconciler wants to exist.
type DesiredLink struct {
	LinkName string // sanitized name (direct child of Target)
	Target   string // target root dir (skills root or project .claude/skills)
	Source   string // absolute source skill dir
}

// Manager owns link operations for one central installation.
type Manager struct {
	// reposRoot is where tracked repos are cloned; a link pointing under it is
	// recognizable as one the tool plausibly created (signature for adoption).
	reposRoot string
	// personalStore is the managed adopted-skill root (U5). Links pointing under
	// it are equally ours — adopted skills live here, and the in-place symlink
	// left behind by adoption points into it, so ownership detection must accept
	// both roots or re-adoption would mistake our own link for a foreign one.
	personalStore string
}

// NewManager returns a Manager. reposRoot is the central repos directory;
// personalStore is the adopted-skill store (may be empty when not configured).
func NewManager(reposRoot, personalStore string) *Manager {
	if abs, err := filepath.Abs(reposRoot); err == nil {
		reposRoot = abs
	}
	if personalStore != "" {
		if abs, err := filepath.Abs(personalStore); err == nil {
			personalStore = abs
		}
	}
	return &Manager{reposRoot: reposRoot, personalStore: personalStore}
}

func targetPath(d DesiredLink) string { return filepath.Join(d.Target, d.LinkName) }

func findRecord(m *config.Manifest, target, name string) *config.LinkRecord {
	for i := range m.Links {
		if m.Links[i].Target == target && m.Links[i].Name == name {
			return &m.Links[i]
		}
	}
	return nil
}

func removeRecord(m *config.Manifest, target, name string) {
	out := m.Links[:0]
	for _, r := range m.Links {
		if r.Target == target && r.Name == name {
			continue
		}
		out = append(out, r)
	}
	m.Links = out
}

func upsertRecord(m *config.Manifest, rec config.LinkRecord) {
	if r := findRecord(m, rec.Target, rec.Name); r != nil {
		*r = rec
		return
	}
	m.Links = append(m.Links, rec)
}

// Link ensures d exists on disk and is recorded in manifest. It is idempotent
// and never clobbers a non-owned path. Returns whether a link was (re)created.
func (mgr *Manager) Link(d DesiredLink, manifest *config.Manifest) (created bool, err error) {
	tp := targetPath(d)
	src, err := filepath.Abs(d.Source)
	if err != nil {
		return false, fmt.Errorf("resolve source: %w", err)
	}
	d.Source = src

	lst, statErr := os.Lstat(tp)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		return mgr.createAndRecord(d, manifest)

	case statErr != nil:
		return false, fmt.Errorf("lstat %s: %w", tp, statErr)

	case isLinkMode(lst.Mode()):
		rec := findRecord(manifest, d.Target, d.LinkName)
		if rec != nil {
			if rec.Source == d.Source && linkPointsAt(tp, d.Source) {
				return false, nil // idempotent: correct owned link already present
			}
			// owned but stale/wrong-source — re-create
			if err := removePrimitive(tp, rec.LinkType); err != nil {
				return false, err
			}
			removeRecord(manifest, d.Target, d.LinkName)
			return mgr.createAndRecord(d, manifest)
		}
		// unowned link: adopt only if it carries our signature (points under
		// reposRoot/personalStore). This is the 4th ownership channel beyond the
		// manifest (invariant ④, KTD6); it is safe because ~/.skillmanage/{repos,
		// local} is SkillManage's PRIVATE canonical store — no external tool writes
		// links there, so a signature match is necessarily our own leftover link
		// (e.g. a manifest lost across a reinstall). A foreign tool's link (e.g.
		// skills.sh's ~/.claude/skills/x → ~/.agents/skills/x) resolves OUTSIDE our
		// store, so looksOurs is false and we refuse below — never clobbering it.
		if mgr.looksOurs(tp) {
			if err := os.Remove(tp); err != nil {
				return false, fmt.Errorf("remove signature link for adoption: %w", err)
			}
			return mgr.createAndRecord(d, manifest)
		}
		// never-break (invariant ④): a foreign link is refused, never removed.
		return false, fmt.Errorf("%w: %s is a foreign link", ErrTargetOccupied, tp)

	default:
		// a real file or directory
		if rec := findRecord(manifest, d.Target, d.LinkName); rec != nil {
			if rec.LinkType == config.LinkCopy {
				// Our copy fallback (KTD12): a copy is a real dir we own. Re-copy
				// every reconcile so it stays fresh (a static copy does not
				// auto-refresh like a link).
				if err := removePrimitive(tp, config.LinkCopy); err != nil {
					return false, err
				}
				removeRecord(manifest, d.Target, d.LinkName)
				return mgr.createAndRecord(d, manifest)
			}
			// We expected a link but found a real dir — refuse to treat as owned.
			return false, fmt.Errorf("%w: %s is now a real path", ErrDiverged, tp)
		}
		return false, fmt.Errorf("%w: %s", ErrTargetOccupied, tp)
	}
}

func (mgr *Manager) createAndRecord(d DesiredLink, manifest *config.Manifest) (bool, error) {
	if err := os.MkdirAll(d.Target, 0o755); err != nil {
		return false, fmt.Errorf("create target dir: %w", err)
	}
	lt, err := createPrimitive(d.Source, targetPath(d))
	if err != nil {
		return false, err
	}
	upsertRecord(manifest, config.LinkRecord{
		Name:     d.LinkName,
		Target:   d.Target,
		Source:   d.Source,
		LinkType: lt,
	})
	return true, nil
}

// Unlink removes a recorded link and drops it from the manifest.
func (mgr *Manager) Unlink(rec config.LinkRecord, manifest *config.Manifest) error {
	tp := filepath.Join(rec.Target, rec.Name)
	if err := removePrimitive(tp, rec.LinkType); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	removeRecord(manifest, rec.Target, rec.Name)
	return nil
}

// PruneDangling removes links whose on-disk link is gone or whose source no
// longer exists, dropping them from the manifest (R15). Returns the removed
// records for UI reporting.
func (mgr *Manager) PruneDangling(manifest *config.Manifest) ([]config.LinkRecord, error) {
	var removed []config.LinkRecord
	kept := manifest.Links[:0:0]
	for _, rec := range manifest.Links {
		tp := filepath.Join(rec.Target, rec.Name)
		lst, statErr := os.Lstat(tp)
		linkGone := errors.Is(statErr, os.ErrNotExist)
		sourceGone := false
		if _, err := os.Stat(rec.Source); errors.Is(err, os.ErrNotExist) {
			sourceGone = true
		}
		// Only prune things that are still our kind of link (or already gone).
		isOurLink := statErr == nil && isLinkMode(lst.Mode())
		// A copy fallback (KTD12) is a real directory we own. Only treat it as
		// our copy — and thus eligible for RemoveAll — when the on-disk path is
		// still a directory. If it diverged into a symlink or file, refuse the
		// destructive RemoveAll (KTD5 never-clobber); keep the record so the
		// divergence stays visible rather than silently nuking the path.
		isOurCopy := statErr == nil && rec.LinkType == config.LinkCopy && lst.IsDir()
		if linkGone {
			removed = append(removed, rec)
			continue
		}
		if sourceGone && (isOurLink || isOurCopy) {
			if err := removePrimitive(tp, rec.LinkType); err != nil && !errors.Is(err, os.ErrNotExist) {
				return removed, err
			}
			removed = append(removed, rec)
			continue
		}
		kept = append(kept, rec)
	}
	manifest.Links = kept
	return removed, nil
}

// ConflictKind classifies a conflict.
type ConflictKind string

const (
	// ConflictCollision: two distinct sources want the same name under the
	// same target — the user must alias one (R13).
	ConflictCollision ConflictKind = "collision"
	// ConflictShadow: the same name is linked under more than one target within
	// the SAME harness; the personal one shadows the project-level one, so the
	// user should know (cross-target shadowing). NOT raised across harnesses —
	// CC and Codex carrying the same name is the intended dual-harness mapping.
	ConflictShadow ConflictKind = "shadow"
	// ConflictNested: a skill source containing a nested SKILL.md is mapped to a
	// Codex target; Codex will register the nested skill independently (#22275),
	// polluting its list. Advisory — the link is still created (KTD6).
	ConflictNested ConflictKind = "nested"
)

// Conflict is a detected naming problem in a desired link set.
type Conflict struct {
	Kind     ConflictKind `json:"kind"`
	LinkName string       `json:"linkName"`
	Targets  []string     `json:"targets"`
	Sources  []string     `json:"sources"`
}

// DetectConflicts inspects a desired link set for within-root collisions and
// cross-target shadowing. It is pure (no filesystem access) so the reconciler
// can surface conflicts before touching disk. Shadowing is scoped per harness
// (KTD1): the same name under CC personal + CC project is a shadow, but the
// same name under CC and Codex is the intended dual-harness mapping, not a
// shadow.
func DetectConflicts(desired []DesiredLink) []Conflict {
	type key struct{ target, name string }
	type shadowKey struct{ harness, name string }
	sourcesByKey := map[key]map[string]bool{}
	targetsByShadow := map[shadowKey]map[string]bool{}

	for _, d := range desired {
		k := key{d.Target, d.LinkName}
		if sourcesByKey[k] == nil {
			sourcesByKey[k] = map[string]bool{}
		}
		sourcesByKey[k][d.Source] = true
		sk := shadowKey{harnessOf(d.Target), d.LinkName}
		if targetsByShadow[sk] == nil {
			targetsByShadow[sk] = map[string]bool{}
		}
		targetsByShadow[sk][d.Target] = true
	}

	var out []Conflict
	for k, srcs := range sourcesByKey {
		if len(srcs) > 1 {
			out = append(out, Conflict{Kind: ConflictCollision, LinkName: k.name, Targets: []string{k.target}, Sources: sortedKeys(srcs)})
		}
	}
	for sk, tgts := range targetsByShadow {
		if len(tgts) > 1 {
			out = append(out, Conflict{Kind: ConflictShadow, LinkName: sk.name, Targets: sortedKeys(tgts)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].LinkName < out[j].LinkName
	})
	return out
}

// harnessOf classifies a target directory by the agent that consumes it, so
// shadow detection only fires within a single harness (KTD1).
func harnessOf(target string) string {
	return string(harness.Classify(target))
}

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// looksOurs reports whether the link at path resolves to a target under the
// repos root or personal store — the signature for adopting an unowned link
// (KTD5, invariant ④/KTD6). This is sound precisely because those roots are
// SkillManage's private canonical store: a link resolving there is ours, a link
// resolving anywhere else (a foreign tool's install) is not and must be left
// untouched. Best-effort: platforms where the target cannot be read return
// false (conservative — Windows junction whose target is unreadable is treated
// as foreign, so we refuse rather than risk clobbering it).
func (mgr *Manager) looksOurs(path string) bool { return mgr.OwnedRoot(path) != "" }

// OwnedRoot returns which private canonical root the link at path resolves under
// — the repos root or the personal store — or "" if it resolves elsewhere or
// cannot be read. It is the exported form of the looksOurs ownership signature
// (invariant ④/KTD6), so other packages (source classification) reuse the SAME
// predicate instead of reimplementing "is this ours?" and risking drift.
func (mgr *Manager) OwnedRoot(path string) string {
	resolved, ok := mgr.ResolveLink(path)
	if !ok {
		return ""
	}
	switch {
	case underRoot(mgr.reposRoot, resolved):
		return mgr.reposRoot
	case mgr.personalStore != "" && underRoot(mgr.personalStore, resolved):
		return mgr.personalStore
	default:
		return ""
	}
}

// ResolveLink returns the cleaned absolute path the link at p points at, using
// the same platform-correct, conservative resolution Link uses (Windows
// junctions whose target is unreadable yield ok=false). ok is false when p is
// not a readable link (a real directory, a broken link, or an unreadable
// junction) — callers treat those as non-owned / unknown.
func (mgr *Manager) ResolveLink(p string) (string, bool) {
	target, err := readLinkTarget(p)
	if err != nil || target == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), target)
	}
	return filepath.Clean(target), true
}

// ReposRoot and PersonalStore expose the private canonical roots so callers can
// map an owned link's resolved target to a source kind (git vs local).
func (mgr *Manager) ReposRoot() string     { return mgr.reposRoot }
func (mgr *Manager) PersonalStore() string { return mgr.personalStore }

// FindOwned looks up a manifest record for (target, name), matching the target
// by resolved path and the name case-insensitively. The case fold matters on
// macOS/Windows: a manifest record keyed "find-skills" must still match an
// on-disk entry named "Find-Skills", or an owned link is misread as unowned.
func (mgr *Manager) FindOwned(m *config.Manifest, target, name string) *config.LinkRecord {
	wantTarget := harness.Expand(target)
	for i := range m.Links {
		if harness.Expand(m.Links[i].Target) == wantTarget && strings.EqualFold(m.Links[i].Name, name) {
			return &m.Links[i]
		}
	}
	return nil
}

// underRoot reports whether target is root or a descendant of root.
func underRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// linkPointsAt reports whether the link at path resolves to want.
func linkPointsAt(path, want string) bool {
	target, err := readLinkTarget(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target) == filepath.Clean(want)
}

// removePrimitive deletes a link or copied tree. For a symlink or junction it
// removes the link itself (os.Remove never follows into the target); for a
// copy it removes the copied tree. os.Remove on a non-empty real directory
// fails, which is the safety we want — it will not silently delete a real dir.
func removePrimitive(path string, lt config.LinkType) error {
	if lt == config.LinkCopy {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// CopyTree copies the directory src to dst, preserving file modes and skipping
// symlinks (never dereferenced). Used as the Windows cross-volume link fallback
// (KTD12) and by the adopt package to duplicate a skill into the personal store.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		// Do not dereference symlinks: copying their target contents could pull
		// in files from outside the source tree. Skip them.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, info.Mode().Perm())
	})
}
