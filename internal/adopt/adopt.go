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
	ID   string `json:"id"`   // sanitized link name; the stable handle the API takes
	Name string `json:"name"` // logical (source dir) name
	Dir  string `json:"dir"`  // absolute source directory
}

// ListAdoptable returns the real (non-symlink) skills under ccSkillsDir that the
// daemon does not already own. scanner.Scan skips symlinks (WalkDir lstat's), so
// links we created are naturally excluded; the manifest check additionally
// excludes copy-fallback entries we own, and guarded dirs are never listed.
func ListAdoptable(ccSkillsDir string, manifest *config.Manifest) ([]Adoptable, error) {
	abs := mustAbs(ccSkillsDir)
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		return nil, nil // no CC skills dir yet → nothing to adopt
	}
	skills, err := scanner.Scan(abs)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, l := range manifest.Links {
		if mustAbs(l.Target) == abs {
			owned[l.Name] = true
		}
	}
	var out []Adoptable
	for _, sk := range skills {
		if owned[sk.LinkName] || harness.Guarded(sk.Dir) {
			continue
		}
		out = append(out, Adoptable{ID: sk.LinkName, Name: sk.LogicalName, Dir: sk.Dir})
	}
	return out, nil
}

// Adopt relocates the skill identified by id (a ListAdoptable ID, never a raw
// caller path) from ccSkillsDir into personalStore and links it back in place.
// mgr must be constructed with the same personalStore so the in-place link is
// recognized as owned. It is idempotent: an already-adopted skill is a no-op.
func Adopt(id, ccSkillsDir, personalStore string, mgr *linker.Manager, manifest *config.Manifest) error {
	if !validID(id) {
		return codeErr("invalid", "illegal skill id %q", id)
	}
	ccAbs := mustAbs(ccSkillsDir)
	storeAbs := mustAbs(personalStore)
	src := filepath.Join(ccAbs, id)
	dst := filepath.Join(storeAbs, id)
	tmp := filepath.Join(storeAbs, ".tmp-"+id)

	// Containment + guard (KTD7): src must be a direct child of the CC skills
	// dir and never a Codex-guarded path, regardless of how id was obtained.
	if filepath.Dir(src) != ccAbs {
		return codeErr("invalid", "source escapes the skills dir: %s", src)
	}
	if harness.Guarded(src) || harness.Guarded(ccAbs) {
		return codeErr("guarded", "refusing to adopt from a guarded directory: %s", src)
	}

	lst, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return codeErr("not_found", "no skill %q under %s", id, ccAbs)
	}
	if err != nil {
		return codeErr("not_found", "stat source: %w", err)
	}
	// Idempotent re-entry: source is already a symlink. If it's ours, done.
	if lst.Mode()&os.ModeSymlink != 0 {
		if isOwnedLink(manifest, ccAbs, id) {
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

	// If a complete copy already exists from a prior interrupted run (rename is
	// atomic, so dst existing ⇒ a complete verified copy), skip straight to the
	// in-place relink. Otherwise copy → verify → atomic rename.
	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
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
	}

	// Data now safely lives in dst. Replace the original real dir with a symlink
	// to it. Removing the original is safe: the store copy is complete.
	if err := os.RemoveAll(src); err != nil {
		return codeErr("link_failed", "remove original after copy (copy safe in store): %w", err)
	}
	if _, err := mgr.Link(linker.DesiredLink{LinkName: id, Target: ccAbs, Source: dst}, manifest); err != nil {
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
		if l.Name == name && mustAbs(l.Target) == targetAbs {
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

func mustAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}
