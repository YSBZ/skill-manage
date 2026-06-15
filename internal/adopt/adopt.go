// Package adopt relocates a user's local Claude Code skill into SkillManage's
// managed personal store and leaves a symlink in its place, so the skill keeps
// working in Claude Code while becoming a managed source the daemon can map to
// any harness (U5, F1).
//
// Data safety is the package's first concern (KTD4): the original is removed
// ONLY after a verified complete copy exists in the store, so no interruption
// can lose the skill. All destructive steps are guarded by path containment and
// the Codex guarded-dir check (KTD7) — defense on the write path, not just in
// the listing.
package adopt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"skillmanage/internal/config"
	"skillmanage/internal/harness"
	"skillmanage/internal/linker"
	"skillmanage/internal/scanner"
)

// Error carries a stable Code the API/UI maps to a user-facing message.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func codeErr(code, format string, a ...any) *Error {
	return &Error{Code: code, Err: fmt.Errorf(format, a...)}
}

// Adoptable is one local skill eligible for adoption.
type Adoptable struct {
	ID      string `json:"id"`                // sanitized link name; the stable handle the API takes
	Name    string `json:"name"`              // logical (source dir) name
	Dir     string `json:"dir"`               // absolute source directory
	Root    string `json:"root"`              // absolute source root the skill lives under; addresses Adopt across roots
	Harness string `json:"harness,omitempty"` // owning agent, for UI labeling
}

// ListAdoptable returns the real (non-symlink) skills under each personal target
// root that the daemon does not already own. Every sync target is a
// "bidirectional" directory (KTD1): the same dir is where the agent loads skills
// from, where we drop our symlinks, and where the user hand-authors skills — so
// the adopt scan root IS the link target. scanner.ScanShallow looks at direct
// children only (no deep walk) and skips symlinks, so links we created are
// naturally excluded and a broad dir doesn't surface nested plugin skills; the
// manifest check additionally excludes copy-fallback entries we own, and guarded
// dirs (Codex .system / vendor_imports/skills) are never listed.
func ListAdoptable(roots []harness.Target, manifest *config.Manifest) ([]Adoptable, error) {
	var out []Adoptable
	seenRoot := map[string]bool{}
	for _, t := range roots {
		abs := harness.Expand(t.Dir)
		if seenRoot[abs] {
			continue // CC and Codex differ, but guard against duplicate targets
		}
		seenRoot[abs] = true
		if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
			continue // this agent has no skills dir yet → nothing to adopt here
		}
		// Direct children only: an adoptable skill sits right under the target,
		// matching Adopt's containment rule. A deep walk here would dredge up
		// every nested plugin skill when a broad dir (e.g. ~/.claude) is added.
		skills, err := scanner.ScanShallow(abs)
		if err != nil {
			return nil, err
		}
		owned := map[string]bool{}
		for _, l := range manifest.Links {
			if harness.Expand(l.Target) == abs {
				owned[l.Name] = true
			}
		}
		for _, sk := range skills {
			// Plugin skills (under a .../plugins/... path) are managed by the
			// agent's own plugin system, not hand-authored — never offer them for
			// adoption.
			if owned[sk.LinkName] || harness.Guarded(sk.Dir) || underPlugins(sk.Dir) {
				continue
			}
			out = append(out, Adoptable{ID: sk.LinkName, Name: sk.LogicalName, Dir: sk.Dir, Root: abs, Harness: string(t.Harness)})
		}
	}
	return out, nil
}

// underPlugins reports whether p has a path component named "plugins" — the
// convention for agent plugin trees (~/.claude/plugins/…, codex equivalents).
func underPlugins(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == "plugins" {
			return true
		}
	}
	return false
}

// Adopt relocates the skill identified by id (a ListAdoptable ID, never a raw
// caller path) from sourceRoot — one of the personal target dirs, e.g. CC's or
// Codex's — into personalStore and links it back in place. mgr must be
// constructed with the same personalStore so the in-place link is recognized as
// owned. It is idempotent: an already-adopted skill is a no-op.
func Adopt(id, sourceRoot, personalStore string, mgr *linker.Manager, manifest *config.Manifest) error {
	if !validID(id) {
		return codeErr("invalid", "illegal skill id %q", id)
	}
	rootAbs := harness.Expand(sourceRoot)
	storeAbs := harness.Expand(personalStore)
	src := filepath.Join(rootAbs, id)
	dst := filepath.Join(storeAbs, id)
	tmp := filepath.Join(storeAbs, ".tmp-"+id)

	// Containment + guard (KTD7): src must be a direct child of the source root
	// and never a Codex-guarded path, regardless of how id was obtained.
	if filepath.Dir(src) != rootAbs {
		return codeErr("invalid", "source escapes the skills dir: %s", src)
	}
	if harness.Guarded(src) || harness.Guarded(rootAbs) {
		return codeErr("guarded", "refusing to adopt from a guarded directory: %s", src)
	}

	lst, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return codeErr("not_found", "no skill %q under %s", id, rootAbs)
	}
	if err != nil {
		return codeErr("not_found", "stat source: %w", err)
	}
	// Idempotent re-entry: source is already a symlink. If it's ours, done.
	if lst.Mode()&os.ModeSymlink != 0 {
		if isOwnedLink(manifest, rootAbs, id) {
			return nil
		}
		return codeErr("invalid", "source is a symlink we do not own: %s", src)
	}
	if !lst.IsDir() {
		return codeErr("invalid", "source is not a directory: %s", src)
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return codeErr("invalid", "source has no SKILL.md: %s", src)
	}

	if err := os.MkdirAll(storeAbs, 0o755); err != nil {
		return codeErr("copy_failed", "create personal store: %w", err)
	}

	// Decide what an existing dst means. The personal store is flat, so two
	// roots can carry the same name (e.g. CC/foo and Codex/foo both → store/foo).
	// verifyTreeMatch discriminates the two cases an existing dst can be:
	//   - legit re-entry after a crash post-rename (rename is atomic ⇒ dst is a
	//     complete copy of THIS src) → contents match → resume the relink;
	//   - a DIFFERENT skill of the same name already adopted from another root →
	//     contents differ → refuse rather than silently clobber/lose data.
	// When dst is absent, do the normal copy → verify → atomic rename.
	switch _, statErr := os.Stat(dst); {
	case errors.Is(statErr, os.ErrNotExist):
		_ = os.RemoveAll(tmp) // clear any leftover temp from a prior crash
		if err := linker.CopyTree(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("copy_failed", "copy into store: %w", err)
		}
		if err := verifyTreeMatch(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("verify_failed", "incomplete copy, original left untouched: %w", err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("copy_failed", "finalize store copy: %w", err)
		}
	case statErr != nil:
		return codeErr("copy_failed", "stat store destination: %w", statErr)
	default:
		if err := verifyTreeMatch(src, dst); err != nil {
			return codeErr("name_taken", "a different skill named %q already exists in the personal store; rename one before adopting (original left untouched): %w", id, err)
		}
	}

	// Data now safely lives in dst. Replace the original real dir with a symlink
	// to it. Removing the original is safe: the store copy is complete.
	if err := os.RemoveAll(src); err != nil {
		return codeErr("link_failed", "remove original after copy (copy safe in store): %w", err)
	}
	if _, err := mgr.Link(linker.DesiredLink{LinkName: id, Target: rootAbs, Source: dst}, manifest); err != nil {
		// Original already gone; data is intact in the store. Surface that the
		// in-place link must be retried rather than implying data loss.
		return codeErr("rollback_partial", "skill copied to store but relink failed; re-run adopt to restore the link: %w", err)
	}
	return nil
}

// --- helpers ---

func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.HasPrefix(id, "@") {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

func isOwnedLink(m *config.Manifest, targetAbs, name string) bool {
	for _, l := range m.Links {
		if l.Name == name && harness.Expand(l.Target) == targetAbs {
			return true
		}
	}
	return false
}

// verifyTreeMatch confirms dst contains every regular file src has, with equal
// sizes. Symlinks are skipped on both sides (CopyTree does not copy them), and
// directories are ignored — a non-empty dst with the right SKILL.md but missing
// sibling files would otherwise pass a naive check and lose data on delete.
func verifyTreeMatch(src, dst string) error {
	srcFiles, err := fileSizes(src)
	if err != nil {
		return err
	}
	dstFiles, err := fileSizes(dst)
	if err != nil {
		return err
	}
	if len(srcFiles) != len(dstFiles) {
		return fmt.Errorf("file count mismatch: src=%d dst=%d", len(srcFiles), len(dstFiles))
	}
	for rel, sz := range srcFiles {
		dsz, ok := dstFiles[rel]
		if !ok {
			return fmt.Errorf("missing file in copy: %s", rel)
		}
		if dsz != sz {
			return fmt.Errorf("size mismatch for %s: src=%d dst=%d", rel, sz, dsz)
		}
	}
	return nil
}

func fileSizes(root string) (map[string]int64, error) {
	out := map[string]int64{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[rel] = info.Size()
		return nil
	})
	return out, err
}
