// Package reconcile turns the user's enabled[] selections into the desired set
// of links and applies the diff against the ownership manifest (U6). It honors
// follow vs snapshot semantics (R9/R10), resolves per-entry targets (R11),
// prunes dangling links (R15), and surfaces conflicts (R13 + shadowing) and a
// per-cycle change summary so follow-mode additions are visible (R9), not
// silent.
package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"skillmanage/internal/config"
	"skillmanage/internal/harness"
	"skillmanage/internal/linker"
	"skillmanage/internal/scanner"
)

// Summary is the observable outcome of one reconcile cycle.
type Summary struct {
	Created   []config.LinkRecord `json:"created"`
	Removed   []config.LinkRecord `json:"removed"`
	Pruned    []config.LinkRecord `json:"pruned"`
	Conflicts []linker.Conflict   `json:"conflicts"`
	Errors    []string            `json:"errors"`
	// Stale lists enabled selectors whose snapshot skill no longer exists in its
	// (successfully-scanned) repo — dead entries left by an earlier move/delete.
	// They are NOT errors; the caller may prune them from config (self-heal).
	Stale []string `json:"stale,omitempty"`
}

// LocalNamespace is the reserved source-selector namespace for adopted skills
// living in the personal store (U5). `@local/<skill>` resolves under the store
// instead of the repos root. The leading `@` cannot collide with a real repo
// name because ValidRepoName rejects any name starting with `@`.
const LocalNamespace = "@local"

// AgentsNamespace is the reserved selector namespace for skills.sh-managed skills
// living in the shared ~/.agents/skills directory. `@agents/<skill>` links the
// skill into a target WITHOUT touching skills.sh's own files (we only create our
// own link, recorded in the manifest). The canonical content stays owned by
// skills.sh (invariant ④ never-break); we just point at it.
const AgentsNamespace = "@agents"

// DirNamespacePrefix tags a selector that resolves to a user-registered local
// directory source: "@dir:<id>/<skill>" (or "@dir:<id>/*" to follow). The id maps
// to an on-disk path via the dirSources registry (SetDirSources). We link FROM
// these folders but never modify them (invariant ②/④) — same posture as @agents.
const DirNamespacePrefix = "@dir:"

// DirSelector builds the namespace token for a local directory source id.
func DirSelector(id string) string { return DirNamespacePrefix + id }

// Reconciler applies enabled[] to the filesystem under a central repos root.
type Reconciler struct {
	reposRoot     string
	personalStore string
	agentsRoot    string            // ~/.agents/skills, resolved at runtime (skills.sh source)
	dirSources    map[string]string // local directory source id → on-disk path
	mgr           *linker.Manager
}

// New builds a Reconciler. reposRoot is where tracked repos are cloned;
// personalStore is the adopted-skill store (the `@local` namespace root).
func New(reposRoot, personalStore string) *Reconciler {
	return &Reconciler{reposRoot: reposRoot, personalStore: personalStore, mgr: linker.NewManager(reposRoot, personalStore)}
}

// SetAgentsRoot wires the resolved ~/.agents/skills path so `@agents/<skill>`
// selectors resolve. Empty disables the namespace (scan returns nothing).
func (r *Reconciler) SetAgentsRoot(root string) { r.agentsRoot = root }

// SetDirSources wires the local directory-source registry (id → on-disk path) so
// `@dir:<id>/<skill>` selectors resolve. Replace it before each Apply.
func (r *Reconciler) SetDirSources(m map[string]string) { r.dirSources = m }

// sourceRoot resolves a selector namespace to its on-disk root: `@local` → the
// personal store, `@agents` → the skills.sh shared dir, `@dir:<id>` → that
// registered local folder, every other name to reposRoot/<name>.
func (r *Reconciler) sourceRoot(repo string) string {
	switch repo {
	case LocalNamespace:
		return r.personalStore
	case AgentsNamespace:
		return r.agentsRoot
	}
	if id, ok := strings.CutPrefix(repo, DirNamespacePrefix); ok {
		return r.dirSources[id] // "" if unknown → scan yields nothing, links pruned
	}
	return filepath.Join(r.reposRoot, repo)
}

// ValidRepoName reports whether name is a safe single path segment for a repo
// directory under the repos root — no separators, no "." / ".." — so a
// crafted repo selector cannot traverse out of reposRoot (path-traversal
// guard for handleListSkills and computeDesired).
func ValidRepoName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, "@") {
		return false // leading '@' is reserved for source namespaces (e.g. @local)
	}
	return !strings.ContainsAny(name, `/\`)
}

// RepoName derives a repo's on-disk directory name from its URL: the last path
// segment with any ".git" suffix removed.
func RepoName(url string) string {
	u := strings.TrimRight(strings.TrimSpace(url), "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return strings.TrimSuffix(u, ".git")
}

// RepoNameCollides reports whether candidate's on-disk RepoName matches the
// RepoName of any URL in existing (an identical URL is not a collision — that
// is the ordinary duplicate case). Two repos that derive the same directory
// name would share one mirror under the repos root and silently overwrite each
// other on every sync, so adding or importing such a repo must be rejected.
func RepoNameCollides(existing []string, candidate string) bool {
	cn := RepoName(candidate)
	for _, u := range existing {
		if u == candidate {
			continue
		}
		if RepoName(u) == cn {
			return true
		}
	}
	return false
}

// Apply reconciles cfg against manifest, mutating manifest in place. The caller
// persists the manifest afterwards.
func (r *Reconciler) Apply(cfg config.Config, manifest *config.Manifest) Summary {
	var sum Summary

	// 1. Prune dangling first so removed-upstream links don't linger (R15).
	pruned, err := r.mgr.PruneDangling(manifest)
	if err != nil {
		sum.Errors = append(sum.Errors, fmt.Sprintf("prune: %v", err))
	}
	sum.Pruned = pruned

	// 2. Compute desired links from enabled[]. nested holds Codex-target nested
	//    SKILL.md warnings (KTD6), produced where the scanner.Skill is in scope.
	desired, nested, derrs, stale := r.computeDesired(cfg)
	sum.Errors = append(sum.Errors, derrs...)
	sum.Stale = stale

	// 3. Detect conflicts. Collisions (same target+name, different source) are
	//    skipped pending user alias; shadowing links are still created but
	//    surfaced as a warning. Nested-SKILL.md warnings are appended.
	sum.Conflicts = append(linker.DetectConflicts(desired), nested...)
	skip := collisionSkipSet(sum.Conflicts)

	// 4. Create/refresh desired links.
	desiredKeys := map[linkKey]bool{}
	for _, d := range desired {
		k := linkKey{d.Target, d.LinkName}
		// Mark the key desired BEFORE the collision skip so the removal pass in
		// step 5 does not tear down a previously-working link while the user
		// resolves the alias (R13).
		desiredKeys[k] = true
		if skip[k] {
			continue
		}
		created, err := r.mgr.Link(d, manifest)
		if err != nil {
			sum.Errors = append(sum.Errors, fmt.Sprintf("link %s -> %s: %v", filepath.Join(d.Target, d.LinkName), d.Source, err))
			continue
		}
		if created {
			sum.Created = append(sum.Created, config.LinkRecord{Name: d.LinkName, Target: d.Target, Source: d.Source})
		}
	}

	// 5. Remove links no longer desired. Snapshot the manifest first because
	//    Unlink mutates it.
	current := make([]config.LinkRecord, len(manifest.Links))
	copy(current, manifest.Links)
	for _, rec := range current {
		if desiredKeys[linkKey{rec.Target, rec.Name}] {
			continue
		}
		if err := r.mgr.Unlink(rec, manifest); err != nil {
			sum.Errors = append(sum.Errors, fmt.Sprintf("unlink %s: %v", filepath.Join(rec.Target, rec.Name), err))
			continue
		}
		sum.Removed = append(sum.Removed, rec)
	}

	return sum
}

type linkKey struct{ target, name string }

func collisionSkipSet(conflicts []linker.Conflict) map[linkKey]bool {
	skip := map[linkKey]bool{}
	for _, c := range conflicts {
		if c.Kind != linker.ConflictCollision {
			continue
		}
		for _, tgt := range c.Targets {
			skip[linkKey{tgt, c.LinkName}] = true
		}
	}
	return skip
}

// computeDesired expands every enabled entry into desired links, scanning each
// repo at most once. It also returns nested-SKILL.md conflicts for sources
// mapped to a Codex target (KTD6) — produced here because the scanner.Skill
// (which carries HasNested) is in scope, whereas linker.DetectConflicts only
// sees DesiredLink.
func (r *Reconciler) computeDesired(cfg config.Config) ([]linker.DesiredLink, []linker.Conflict, []string, []string) {
	var desired []linker.DesiredLink
	var nested []linker.Conflict
	var errs []string
	var stale []string

	noteNested := func(sk scanner.Skill, target string) {
		if sk.HasNested && harness.IsCodexTarget(target) {
			nested = append(nested, linker.Conflict{
				Kind:     linker.ConflictNested,
				LinkName: sk.LinkName,
				Targets:  []string{target},
				Sources:  []string{sk.Dir},
			})
		}
	}

	scanCache := map[string][]scanner.Skill{}
	scanErr := map[string]bool{}
	getSkills := func(repo string) []scanner.Skill {
		if s, ok := scanCache[repo]; ok {
			return s
		}
		dir := r.sourceRoot(repo)
		skills, err := scanner.Scan(dir)
		if err != nil {
			if !scanErr[repo] {
				errs = append(errs, fmt.Sprintf("scan repo %q: %v", repo, err))
				scanErr[repo] = true
			}
			scanCache[repo] = nil
			return nil
		}
		scanCache[repo] = skills
		return skills
	}

	for _, e := range cfg.Enabled {
		repo, sel := splitSkill(e.Skill)
		isDir := strings.HasPrefix(repo, DirNamespacePrefix)
		if repo == "" || (repo != LocalNamespace && repo != AgentsNamespace && !isDir && !ValidRepoName(repo)) {
			errs = append(errs, fmt.Sprintf("invalid enabled skill selector %q", e.Skill))
			continue
		}
		target, terr := expandTarget(e.Target)
		if terr != nil {
			errs = append(errs, fmt.Sprintf("resolve target %q: %v", e.Target, terr))
			continue
		}
		skills := getSkills(repo)
		if sel == "*" {
			for _, sk := range skills {
				desired = append(desired, linker.DesiredLink{LinkName: sk.LinkName, Target: target, Source: sk.Dir})
				noteNested(sk, target)
			}
			continue
		}
		// snapshot: match a single skill by link name or logical name
		var matched *scanner.Skill
		for i := range skills {
			if skills[i].LinkName == sel || skills[i].LogicalName == sel {
				matched = &skills[i]
				break
			}
		}
		if matched == nil {
			// The repo scanned fine but the skill is gone (moved/deleted earlier) →
			// a dead enabled entry, not a sync failure. Report it as stale so the
			// caller can self-heal; don't fail the whole sync. When the repo itself
			// failed to scan, getSkills already recorded that error — skip both.
			if !scanErr[repo] {
				stale = append(stale, e.Skill)
			}
			continue
		}
		desired = append(desired, linker.DesiredLink{LinkName: matched.LinkName, Target: target, Source: matched.Dir})
		noteNested(*matched, target)
	}

	// Precedence: a git-repo skill wins over an @local (personal/adopted) skill
	// of the same name on the same target. Drop the @local link so reconcile
	// links the git version, not a collision-skip of both.
	repoKeys := map[linkKey]bool{}
	for _, d := range desired {
		if !underDir(d.Source, r.personalStore) {
			repoKeys[linkKey{d.Target, d.LinkName}] = true
		}
	}
	kept := desired[:0]
	for _, d := range desired {
		if underDir(d.Source, r.personalStore) && repoKeys[linkKey{d.Target, d.LinkName}] {
			continue // @local shadowed by a same-named git skill → git wins
		}
		kept = append(kept, d)
	}
	desired = kept
	return desired, nested, errs, stale
}

// underDir reports whether path is root or a descendant of it.
func underDir(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// splitSkill splits "repo/sel" into ("repo", "sel"). sel may be "*".
func splitSkill(s string) (repo, sel string) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "/")
	if i < 0 {
		return "", ""
	}
	return s[:i], s[i+1:]
}

// expandTarget resolves a leading ~ and returns an absolute, cleaned dir.
func expandTarget(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", fmt.Errorf("empty target")
	}
	if t == "~" || strings.HasPrefix(t, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		t = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(t, "~"), "/"))
	}
	abs, err := filepath.Abs(t)
	if err != nil {
		return "", err
	}
	return abs, nil
}
