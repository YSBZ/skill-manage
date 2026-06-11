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
}

// NewManager returns a Manager. reposRoot is the central repos directory.
func NewManager(reposRoot string) *Manager {
	abs, err := filepath.Abs(reposRoot)
	if err == nil {
		reposRoot = abs
	}
	return &Manager{reposRoot: reposRoot}
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
		// unowned link: adopt only if it carries our signature (points under reposRoot)
		if mgr.looksOurs(tp) {
			if err := os.Remove(tp); err != nil {
				return false, fmt.Errorf("remove signature link for adoption: %w", err)
			}
			return mgr.createAndRecord(d, manifest)
		}
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
		if linkGone {
			removed = append(removed, rec)
			continue
		}
		if sourceGone && (isOurLink || rec.LinkType == config.LinkCopy) {
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
	// ConflictShadow: the same name is linked under more than one target; CC
	// shadows the project-level one (personal-over-project), so the user should
	// know (cross-target shadowing).
	ConflictShadow ConflictKind = "shadow"
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
// can surface conflicts before touching disk.
func DetectConflicts(desired []DesiredLink) []Conflict {
	type key struct{ target, name string }
	sourcesByKey := map[key]map[string]bool{}
	targetsByName := map[string]map[string]bool{}

	for _, d := range desired {
		k := key{d.Target, d.LinkName}
		if sourcesByKey[k] == nil {
			sourcesByKey[k] = map[string]bool{}
		}
		sourcesByKey[k][d.Source] = true
		if targetsByName[d.LinkName] == nil {
			targetsByName[d.LinkName] = map[string]bool{}
		}
		targetsByName[d.LinkName][d.Target] = true
	}

	var out []Conflict
	for k, srcs := range sourcesByKey {
		if len(srcs) > 1 {
			out = append(out, Conflict{Kind: ConflictCollision, LinkName: k.name, Targets: []string{k.target}, Sources: sortedKeys(srcs)})
		}
	}
	for name, tgts := range targetsByName {
		if len(tgts) > 1 {
			out = append(out, Conflict{Kind: ConflictShadow, LinkName: name, Targets: sortedKeys(tgts)})
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

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// looksOurs reports whether the link at path resolves to a target under the
// repos root — the signature for adopting an unowned link (KTD5). Best-effort:
// platforms where the target cannot be read return false (conservative).
func (mgr *Manager) looksOurs(path string) bool {
	target, err := readLinkTarget(path)
	if err != nil || target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(mgr.reposRoot, target)
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

// copyTree copies the directory src to dst, preserving file modes. Used as the
// Windows cross-volume fallback and the platform-spike fallback (KTD12).
func copyTree(src, dst string) error {
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
