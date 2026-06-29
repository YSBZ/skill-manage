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
	Plugin  bool   `json:"plugin,omitempty"`  // true when sourced from a plugin tree (import-copy, not relocate)
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
func ListAdoptable(roots []harness.Target, manifest *config.Manifest, includePlugins bool, personalStore string) ([]Adoptable, error) {
	storeAbs := ""
	if strings.TrimSpace(personalStore) != "" {
		storeAbs = harness.Expand(personalStore)
	}
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
			// agent's own plugin system, not hand-authored — ignored unless the
			// user opts to include them.
			if owned[sk.LinkName] || harness.Guarded(sk.Dir) || (!includePlugins && underPlugins(sk.Dir)) {
				continue
			}
			out = append(out, Adoptable{ID: sk.LinkName, Name: sk.LogicalName, Dir: sk.Dir, Root: abs, Harness: string(t.Harness)})
		}

		// When opted in, also surface skills from this target's agent plugin tree
		// (deep walk, since plugin skills are nested). They are import-copied, not
		// relocated, so the plugin cache is left untouched.
		if includePlugins {
			pluginRoot := harness.PluginRootFor(t.Dir)
			if _, statErr := os.Stat(pluginRoot); statErr == nil {
				pskills, perr := scanner.Scan(pluginRoot)
				if perr != nil {
					return nil, perr
				}
				for _, sk := range pskills {
					if harness.Guarded(sk.Dir) {
						continue
					}
					// Skip ones already imported into the store (an @local copy
					// exists) so a 收编'd plugin skill doesn't linger as adoptable.
					if storeAbs != "" {
						if _, err := os.Stat(filepath.Join(storeAbs, sk.LinkName)); err == nil {
							continue
						}
					}
					out = append(out, Adoptable{ID: sk.LinkName, Name: sk.LogicalName, Dir: sk.Dir, Root: pluginRoot, Harness: string(t.Harness), Plugin: true})
				}
			}
		}
	}
	return out, nil
}

// Import copies a skill directory into personalStore under id WITHOUT removing
// the original — used for plugin skills, whose source is owned by the agent's
// plugin system and must stay put. It reuses the same copy→verify→atomic-rename
// safety as Adopt and is idempotent: an identical store entry is a no-op, a
// different skill of the same name is refused (name_taken). The caller maps the
// resulting @local/<id> into a real sync target via an enabled entry.
func Import(srcDir, id, personalStore string) error {
	if !validID(id) {
		return codeErr("invalid", "illegal skill id %q", id)
	}
	src := harness.Expand(srcDir)
	if harness.Guarded(src) {
		return codeErr("guarded", "refusing to import from a guarded directory: %s", src)
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return codeErr("invalid", "source has no SKILL.md: %s", src)
	}
	storeAbs := harness.Expand(personalStore)
	dst := filepath.Join(storeAbs, id)
	tmp := filepath.Join(storeAbs, ".tmp-"+id)
	if err := os.MkdirAll(storeAbs, 0o755); err != nil {
		return codeErr("copy_failed", "create personal store: %w", err)
	}
	switch _, statErr := os.Stat(dst); {
	case errors.Is(statErr, os.ErrNotExist):
		_ = os.RemoveAll(tmp)
		if err := linker.CopyTree(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("copy_failed", "copy into store: %w", err)
		}
		if err := verifyTreeMatch(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("verify_failed", "incomplete copy: %w", err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("copy_failed", "finalize store copy: %w", err)
		}
	case statErr != nil:
		return codeErr("copy_failed", "stat store destination: %w", statErr)
	default:
		if err := verifyTreeMatch(src, dst); err != nil {
			return codeErr("name_taken", "a different skill named %q already exists in the personal store; rename one before importing: %w", id, err)
		}
	}
	return nil
}

// CopyInto copies skill id from srcRoot into destRoot using the copy → verify →
// atomic-rename safety, KEEPING the source intact. It is the durable first half
// of a two-sided move (copy to dest, push dest, only then remove source) so a
// failure before the destination is safe never loses the skill. It refuses to
// clobber a DIFFERENT same-named skill already in destRoot (name_taken); an
// identical existing copy is treated as a completed prior step (idempotent).
func CopyInto(id, srcRoot, destRoot string) error {
	if !validID(id) {
		return codeErr("invalid", "illegal skill id %q", id)
	}
	srcAbs := harness.Expand(srcRoot)
	destAbs := harness.Expand(destRoot)
	src, err := resolveSourceSkill(srcAbs, id)
	if err != nil {
		return err
	}
	destParent := destSkillParent(destAbs)
	dst := filepath.Join(destParent, id)
	tmp := filepath.Join(destParent, ".tmp-"+id)

	if !withinRoot(srcAbs, src) {
		return codeErr("invalid", "source escapes its root: %s", src)
	}
	if harness.Guarded(src) {
		return codeErr("guarded", "refusing to copy from a guarded directory: %s", src)
	}
	lst, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return codeErr("not_found", "no skill %q under %s", id, srcAbs)
	}
	if err != nil {
		return codeErr("not_found", "stat source: %w", err)
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		return codeErr("invalid", "source is a symlink, not a real skill: %s", src)
	}
	if !lst.IsDir() {
		return codeErr("invalid", "source is not a directory: %s", src)
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return codeErr("invalid", "source has no SKILL.md: %s", src)
	}
	if err := os.MkdirAll(destParent, 0o755); err != nil {
		return codeErr("copy_failed", "create destination skills dir: %w", err)
	}
	switch _, statErr := os.Stat(dst); {
	case errors.Is(statErr, os.ErrNotExist):
		_ = os.RemoveAll(tmp)
		if err := linker.CopyTree(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("copy_failed", "copy into destination: %w", err)
		}
		if err := verifyTreeMatch(src, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("verify_failed", "incomplete copy, original left untouched: %w", err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.RemoveAll(tmp)
			return codeErr("copy_failed", "finalize destination copy: %w", err)
		}
	case statErr != nil:
		return codeErr("copy_failed", "stat destination: %w", statErr)
	default:
		if err := verifyTreeMatch(src, dst); err != nil {
			return codeErr("name_taken", "the destination already has a different skill named %q (original left untouched): %w", id, err)
		}
	}
	return nil
}

// MoveInto copies id from srcRoot into destRoot (CopyInto) and then removes the
// source — leaving NO in-place symlink. It backs "移动 @local skill 到 git 仓":
// the store copy vanishes entirely so the skill is re-sourced from the git repo
// (the caller migrates the @local/<id> enabled entries to <repo>/<id> and
// reconciles, which rebuilds the links from git).
func MoveInto(id, srcRoot, destRoot string) error {
	if err := CopyInto(id, srcRoot, destRoot); err != nil {
		return err
	}
	src, err := resolveSourceSkill(harness.Expand(srcRoot), id)
	if err != nil {
		return nil // already copied to destination; source already gone is fine
	}
	if err := os.RemoveAll(src); err != nil {
		return codeErr("link_failed", "remove source after copy (copy safe in destination): %w", err)
	}
	return nil
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
	return Relocate(id, sourceRoot, personalStore, mgr, manifest)
}

// Relocate is the general form behind Adopt: it moves the skill id from
// sourceRoot into destRoot (the personal store for 备份, or a cloned git repo's
// working tree for 贡献到 git 仓) using the same copy → verify → atomic-rename
// safety, then links it back in place. destRoot must already exist (it does for
// both a created store and a cloned repo). It is idempotent and refuses to
// clobber a DIFFERENT skill that already occupies destRoot/id (name_taken),
// which for a git target means the repo already ships a skill by that name.
func Relocate(id, sourceRoot, destRoot string, mgr *linker.Manager, manifest *config.Manifest) error {
	if !validID(id) {
		return codeErr("invalid", "illegal skill id %q", id)
	}
	rootAbs := harness.Expand(sourceRoot)
	destAbs := harness.Expand(destRoot)
	src, rerr := resolveSourceSkill(rootAbs, id)
	if rerr != nil {
		return rerr
	}
	destParent := destSkillParent(destAbs)
	dst := filepath.Join(destParent, id)
	tmp := filepath.Join(destParent, ".tmp-"+id)

	// Containment + guard (KTD7): src must stay within the source root (a stray
	// "../" must never escape) and never be a Codex-guarded path, regardless of
	// how id was obtained.
	if !withinRoot(rootAbs, src) {
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

	if err := os.MkdirAll(destParent, 0o755); err != nil {
		return codeErr("copy_failed", "create destination skills dir: %w", err)
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

// destSkillParent is the directory under destRoot where a NEW skill folder
// should be created, honoring the repo's existing layout (scanner.SkillRoot):
// a repo that keeps skills under "skills/" gets destRoot/skills, a flat repo or
// the personal store gets destRoot itself. The .tmp- staging dir lives here too
// so the finalize rename stays on one filesystem.
func destSkillParent(destAbs string) string {
	if root := scanner.SkillRoot(destAbs); root != "" {
		return filepath.Join(destAbs, filepath.FromSlash(root))
	}
	return destAbs
}

// resolveSourceSkill returns the real directory of skill id under rootAbs. A flat
// layout (rootAbs/id) is used directly; otherwise the root is scanned for a skill
// whose link or logical name is id, so a nested source (e.g. skills/id in a git
// repo) resolves correctly. Returns not_found when neither locates it.
func resolveSourceSkill(rootAbs, id string) (string, error) {
	direct := filepath.Join(rootAbs, id)
	if fi, err := os.Lstat(direct); err == nil && (fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
		return direct, nil
	}
	skills, _ := scanner.Scan(rootAbs)
	for _, sk := range skills {
		if sk.LinkName == id || sk.LogicalName == id {
			return sk.Dir, nil
		}
	}
	return "", codeErr("not_found", "no skill %q under %s", id, rootAbs)
}

// withinRoot reports whether p is rootAbs itself or a descendant of it — the
// containment guard that replaces the old direct-child check now that a source
// skill may be nested. It rejects any path that would escape via "..".
func withinRoot(rootAbs, p string) bool {
	rel, err := filepath.Rel(rootAbs, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

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
