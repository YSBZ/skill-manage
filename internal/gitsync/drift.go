package gitsync

import (
	"context"
	"sort"
	"strings"

	"skillmanage/internal/scanner"
)

// DriftKind classifies one skill's local divergence from upstream.
type DriftKind string

const (
	DriftAdded     DriftKind = "added"     // 新增未推送：untracked dir, never committed
	DriftModified  DriftKind = "modified"  // 修改未推送：tracked file changed in working tree
	DriftDeleted   DriftKind = "deleted"   // 删除未推送：tracked file/dir removed in working tree
	DriftCommitted DriftKind = "committed" // 已提交未推送：commit on HEAD not on upstream (push pending/failed)
)

// DriftEntry is one skill's local change. Skill is the display name (the skill
// directory's own name); Path is the repo-relative directory of the skill (which
// may be nested, e.g. "skills/foo") — git operations stage by Path so they work
// regardless of where the repo keeps its skills. A repo-root file not
// attributable to a skill has Skill == "" (still counts as drift).
type DriftEntry struct {
	Skill string    `json:"skill"`
	Path  string    `json:"path"`
	Kind  DriftKind `json:"kind"`
}

// Drift is the per-repo local-change summary. Dirty true means the repo has
// unpushed local changes (working tree and/or committed-unpushed) and must not
// be reset --hard'd without the user's explicit consent (R8–R11).
type Drift struct {
	Dirty   bool         `json:"dirty"`
	Entries []DriftEntry `json:"entries,omitempty"`
	// Secrets are repo-relative paths of CHANGED files whose names look like a
	// credential or private key (.env, *.pem, id_rsa, .npmrc …). They are NOT
	// silently ignored — that could hide a file the user means to push or give
	// false safety — but surfaced so the contribute dialog can warn and require
	// explicit confirmation before they reach a shared repo's permanent history.
	Secrets []string `json:"secrets,omitempty"`
}

// SecretsUnder returns the flagged secret/credential paths that live under
// repoPath (a repo-relative skill directory). Used to block a single-skill push
// that would carry a credential — while the full-repo dialog can still
// confirm-and-push the same files explicitly.
func (d Drift) SecretsUnder(repoPath string) []string {
	repoPath = strings.TrimSuffix(repoPath, "/")
	var out []string
	for _, s := range d.Secrets {
		if s == repoPath || strings.HasPrefix(s, repoPath+"/") {
			out = append(out, s)
		}
	}
	return out
}

// Has reports whether skill (matched by display name or repo-relative path) has
// any drift entry.
func (d Drift) Has(skill string) (DriftKind, bool) {
	for _, e := range d.Entries {
		if e.Skill == skill || e.Path == skill {
			return e.Kind, true
		}
	}
	return "", false
}

// Drift reports local-change detail for a mirror relative to its upstream ref
// (origin/HEAD when branch is empty, else origin/<branch>). It is purely local
// — no fetch — so the UI can call it on demand (e.g. after the user edits a
// skill) and immediately offer upload without waiting for a sync.
func (s *Syncer) Drift(ctx context.Context, dir, branch string) Drift {
	ref := "origin/HEAD"
	if b := strings.TrimSpace(branch); b != "" {
		ref = "origin/" + b
	}
	return s.driftAt(ctx, dir, ref)
}

// kindRank orders the categories by how much work they need (most first), so a
// skill that is both committed-unpushed AND working-tree-modified shows the more
// actionable kind. A deletion ranks above committed/added so a removed skill is
// never masked by a stray sibling change.
var kindRank = map[DriftKind]int{DriftModified: 4, DriftDeleted: 3, DriftCommitted: 2, DriftAdded: 1}

func (s *Syncer) driftAt(ctx context.Context, dir, ref string) Drift {
	// Make git ignore OS junk (.DS_Store etc.) before reading status, so it never
	// shows up as drift and never gets contributed upstream. Idempotent.
	ensureExcludes(dir)
	// skillRoot is where this repo keeps its skills (e.g. "skills"); changed paths
	// are mapped to the skill DIRECTORY beneath it so attribution is correct for
	// both root-level and nested layouts.
	skillRoot := scanner.SkillRoot(dir)
	best := map[string]DriftKind{}
	secrets := map[string]bool{} // raw repo-relative paths of changed files that look like secrets
	set := func(path string, k DriftKind) {
		if cur, ok := best[path]; !ok || kindRank[k] > kindRank[cur] {
			best[path] = k
		}
	}

	// Working tree: porcelain v1 lines are "XY path"; untracked is "?? path"; a
	// 'D' in either status column is a deletion.
	if out, _, err := s.run(ctx, dir, "status", "--porcelain"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == "" || len(line) < 4 {
				continue
			}
			code, path := line[:2], renameDest(strings.TrimSpace(line[3:]))
			if looksSecret(path) {
				secrets[path] = true
			}
			sp := skillPathOf(path, skillRoot)
			switch {
			case code == "??":
				set(sp, DriftAdded)
			case strings.Contains(code, "D"):
				set(sp, DriftDeleted)
			default:
				set(sp, DriftModified)
			}
		}
	}

	// Committed-unpushed: commits on HEAD not reachable from the upstream ref.
	if cnt, _, err := s.run(ctx, dir, "rev-list", "--count", ref+"..HEAD"); err == nil {
		if c := strings.TrimSpace(cnt); c != "" && c != "0" {
			if names, _, derr := s.run(ctx, dir, "diff", "--name-only", ref+"..HEAD"); derr == nil {
				for _, p := range strings.Split(names, "\n") {
					if p = strings.TrimSpace(p); p != "" {
						if looksSecret(p) {
							secrets[p] = true
						}
						set(skillPathOf(p, skillRoot), DriftCommitted)
					}
				}
			} else {
				set("", DriftCommitted) // commits exist but path enumeration failed
			}
		}
	}

	if len(best) == 0 {
		return Drift{}
	}
	d := Drift{Dirty: true}
	for path, k := range best {
		d.Entries = append(d.Entries, DriftEntry{Skill: lastSeg(path), Path: path, Kind: k})
	}
	sort.Slice(d.Entries, func(i, j int) bool { return d.Entries[i].Path < d.Entries[j].Path })
	for p := range secrets {
		d.Secrets = append(d.Secrets, p)
	}
	sort.Strings(d.Secrets)
	return d
}

// secretSafeTemplates are *.env-style names that are intentionally committable
// (placeholders with no real values), so they must not trip the secret warning.
var secretSafeTemplates = map[string]bool{
	".env.example": true, ".env.sample": true, ".env.template": true,
	".env.dist": true, ".env.defaults": true,
}

// secretExts are file extensions that almost always denote a private key /
// credential bundle. (.pub is deliberately absent — public keys are not secret.)
var secretExts = []string{".pem", ".key", ".p12", ".pfx", ".p8", ".keystore", ".jks", ".asc", ".ppk"}

// looksSecret reports whether a changed file's name strongly suggests a
// credential or private key that should never be pushed to a shared skill repo
// without the user's explicit say-so. Conservative by design: it matches names,
// not contents, and excludes obvious templates — a false positive only adds a
// confirm step, while a miss could leak a secret into permanent git history.
func looksSecret(repoPath string) bool {
	base := strings.ToLower(lastSeg(strings.TrimSuffix(repoPath, "/")))
	if base == "" || secretSafeTemplates[base] {
		return false
	}
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return true
	case base == ".npmrc", base == ".netrc", base == ".pgpass", base == ".git-credentials", base == ".htpasswd":
		return true
	case base == "id_rsa", base == "id_dsa", base == "id_ecdsa", base == "id_ed25519":
		return true
	case strings.Contains(base, "credential"), strings.Contains(base, "secret"):
		// catches credentials.yaml, aws_credentials, client_secret.json, *.secret …
		return true
	}
	for _, ext := range secretExts {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// skillPathOf maps a repo-relative changed-file path to the repo-relative skill
// DIRECTORY it belongs to. With a skillRoot (e.g. "skills"), a path beneath it
// resolves to "skills/<name>"; paths outside it fall back to their first segment
// (covering a stray root-level skill). "" denotes a repo-root file.
func skillPathOf(p, skillRoot string) string {
	p = strings.TrimPrefix(p, "./")
	if skillRoot != "" {
		if p == skillRoot {
			return skillRoot
		}
		if strings.HasPrefix(p, skillRoot+"/") {
			seg := firstSeg(p[len(skillRoot)+1:])
			if seg == "" {
				return skillRoot
			}
			return skillRoot + "/" + seg
		}
	}
	return firstSeg(p)
}

// firstSeg returns the top-level path segment of a repo-relative path, trimming
// any trailing slash git appends to untracked dirs.
func firstSeg(p string) string {
	p = strings.TrimPrefix(p, "./")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// lastSeg returns the final segment of a slash-separated path (the skill's own
// directory name) — the display name for a possibly-nested skill path.
func lastSeg(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// renameDest returns the destination side of a porcelain rename entry
// ("old -> new"), or the path unchanged when it is not a rename.
func renameDest(p string) string {
	if i := strings.Index(p, " -> "); i >= 0 {
		return p[i+4:]
	}
	return p
}
