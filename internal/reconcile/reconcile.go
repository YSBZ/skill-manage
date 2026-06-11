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
}

// Reconciler applies enabled[] to the filesystem under a central repos root.
type Reconciler struct {
	reposRoot string
	mgr       *linker.Manager
}

// New builds a Reconciler. reposRoot is where tracked repos are cloned.
func New(reposRoot string) *Reconciler {
	return &Reconciler{reposRoot: reposRoot, mgr: linker.NewManager(reposRoot)}
}

// ValidRepoName reports whether name is a safe single path segment for a repo
// directory under the repos root — no separators, no "." / ".." — so a
// crafted repo selector cannot traverse out of reposRoot (path-traversal
// guard for handleListSkills and computeDesired).
func ValidRepoName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
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

	// 2. Compute desired links from enabled[].
	desired, derrs := r.computeDesired(cfg)
	sum.Errors = append(sum.Errors, derrs...)

	// 3. Detect conflicts. Collisions (same target+name, different source) are
	//    skipped pending user alias; shadowing links are still created (CC will
	//    shadow the project one) but surfaced as a warning.
	sum.Conflicts = linker.DetectConflicts(desired)
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
// repo at most once.
func (r *Reconciler) computeDesired(cfg config.Config) ([]linker.DesiredLink, []string) {
	var desired []linker.DesiredLink
	var errs []string

	scanCache := map[string][]scanner.Skill{}
	scanErr := map[string]bool{}
	getSkills := func(repo string) []scanner.Skill {
		if s, ok := scanCache[repo]; ok {
			return s
		}
		dir := filepath.Join(r.reposRoot, repo)
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
		if repo == "" || !ValidRepoName(repo) {
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
			errs = append(errs, fmt.Sprintf("skill %q not found in repo %q", sel, repo))
			continue
		}
		desired = append(desired, linker.DesiredLink{LinkName: matched.LinkName, Target: target, Source: matched.Dir})
	}
	return desired, errs
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
