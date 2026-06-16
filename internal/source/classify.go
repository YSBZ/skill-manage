package source

import (
	"os"
	"path/filepath"
	"strings"

	"skillmanage/internal/config"
	"skillmanage/internal/linker"
	"skillmanage/internal/scanner"
)

// Result is the attribution of one skill entry found in a target directory.
type Result struct {
	Kind SourceKind `json:"kind"`
	// Repo is the git repo directory name, set only when Kind == KindGit.
	Repo string `json:"repo,omitempty"`
	// SourceURL is the skills.sh source URL from the lockfile, set only when
	// Kind == KindSkillsSh. UNTRUSTED — validate (http(s) scheme) before display.
	SourceURL string `json:"sourceUrl,omitempty"`
}

// Classifier bundles the per-tab inputs the classifier reuses across entries so
// the per-entry call stays small. Ownership is delegated to the linker.Manager
// (KTD6) — Classifier never reimplements "is this ours?".
type Classifier struct {
	Mgr      *linker.Manager  // private-canonical ownership predicate + roots
	Manifest *config.Manifest // authoritative "we created this" record
	// AgentsSkillsRoot is the resolved ~/.agents/skills dir (skills.sh canonical),
	// or "" when no directory source is registered.
	AgentsSkillsRoot string
	Lock             SkillLock // parsed ~/.agents/.skill-lock.json
	PluginRoots      []string  // resolved plugin trees for the configured targets
	// AllowedRoots bound symlink resolution: a link resolving outside every
	// allowed root is treated as unknown and its target is never read (anti-escape).
	AllowedRoots []string
}

// Classify attributes one skill entry (from ScanShallow of target) to a source.
// Order (KTD2): ownership first (manifest, then private-store signature) for
// BOTH links and real dirs; then for a non-owned symlink, resolve + bound-check
// and apply lockfile > plugin > unknown; a non-owned real dir is handwritten.
func (c Classifier) Classify(entry scanner.Skill, target string) Result {
	// 1. Ownership — unconditional first gate (covers symlinks AND copy-fallback
	//    real dirs). Manifest is authoritative; case-folded for mac/Windows.
	if rec := c.Mgr.FindOwned(c.Manifest, target, entry.LinkName); rec != nil {
		return c.ownedResult(rec.Source)
	}
	// Private-store signature (manifest lost across reinstall). Only meaningful
	// for links; OwnedRoot returns "" for real dirs.
	if root := c.Mgr.OwnedRoot(entry.Dir); root != "" {
		return c.ownedRootResult(root, entry.Dir)
	}

	// 2. Non-owned. Resolve the link target; a real dir (not a link) is handwritten.
	resolved, ok := c.Mgr.ResolveLink(entry.Dir)
	if !ok {
		if isRealDir(entry.Dir) {
			return Result{Kind: KindHandwritten}
		}
		return Result{Kind: KindUnknown} // unreadable / broken link
	}
	// Anti-escape: a link resolving outside every allowed root is unknown, and we
	// never read its target.
	if !underAny(resolved, c.AllowedRoots) {
		return Result{Kind: KindUnknown}
	}
	// lockfile > plugin > unknown (explicit precedence; a link can satisfy both).
	if c.AgentsSkillsRoot != "" && underOne(c.AgentsSkillsRoot, resolved) {
		if e, ok := c.Lock.Has(filepath.Base(resolved)); ok {
			return Result{Kind: KindSkillsSh, SourceURL: e.SourceURL}
		}
		// Under ~/.agents but not in the lockfile → we can't attribute it firmly.
		return Result{Kind: KindUnknown}
	}
	for _, pr := range c.PluginRoots {
		if pr != "" && underOne(pr, resolved) {
			return Result{Kind: KindPlugin}
		}
	}
	return Result{Kind: KindUnknown}
}

// ownedResult maps an owned record's canonical Source path to git/local.
func (c Classifier) ownedResult(sourcePath string) Result {
	return c.ownedRootResultFromSource(sourcePath)
}

// ownedRootResult is the OwnedRoot (signature) path: we know the resolved target
// is under root; derive the repo name from the actual resolved target.
func (c Classifier) ownedRootResult(root, linkPath string) Result {
	if resolved, ok := c.Mgr.ResolveLink(linkPath); ok {
		return c.ownedRootResultFromSource(resolved)
	}
	return c.ownedRootResultFromSource(root)
}

func (c Classifier) ownedRootResultFromSource(sourcePath string) Result {
	reposRoot := c.Mgr.ReposRoot()
	store := c.Mgr.PersonalStore()
	if reposRoot != "" && underOne(reposRoot, sourcePath) {
		return Result{Kind: KindGit, Repo: firstSegment(reposRoot, sourcePath)}
	}
	if store != "" && underOne(store, sourcePath) {
		return Result{Kind: KindLocal}
	}
	// Owned per manifest but source resolves outside both roots — shouldn't
	// happen; report git without a repo rather than crash.
	return Result{Kind: KindGit}
}

// firstSegment returns the first path segment of target relative to root — the
// repo directory name (reposRoot/<repo>/<skill...>).
func firstSegment(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return rel
}

func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0
}

// underOne reports whether target is root or a descendant of root.
func underOne(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func underAny(target string, roots []string) bool {
	for _, r := range roots {
		if r != "" && underOne(r, target) {
			return true
		}
	}
	return false
}
