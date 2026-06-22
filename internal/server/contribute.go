package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"skillmanage/internal/adopt"
	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
	"skillmanage/internal/harness"
	"skillmanage/internal/linker"
	"skillmanage/internal/reconcile"
	"skillmanage/internal/scanner"
)

// commitTicket is the fixed ticket-prefix literal for contribution commits. The
// brainstorm deliberately scoped this as a constant, not a ticket-system input.
const commitTicket = "MIC-0"

// maxCommitDescRunes bounds the editable commit summary so a pasted multi-page
// description cannot bloat the commit subject.
const maxCommitDescRunes = 200

// commitMessage builds "MIC-0 <skill> <description>", falling back to
// "MIC-0 <skill>" when the (sanitized) description is empty.
func commitMessage(skill, desc string) string {
	d := sanitizeCommitDesc(desc)
	if d == "" {
		return commitTicket + " " + skill
	}
	return commitTicket + " " + skill + " " + d
}

// sanitizeCommitDesc folds the description to a single safe line: control
// characters (newlines, tabs, NUL) become spaces, runs of whitespace collapse,
// and the result is rune-truncated. This keeps the commit subject one line and
// prevents a crafted description from injecting extra log lines.
func sanitizeCommitDesc(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == 0 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ") // collapse all whitespace runs
	if r := []rune(s); len(r) > maxCommitDescRunes {
		s = strings.TrimSpace(string(r[:maxCommitDescRunes]))
	}
	return s
}

// safeSkillSeg reports whether name is a single safe path segment for a skill
// directory inside a repo (no separators, no traversal, no @-namespace prefix).
func safeSkillSeg(name string) bool {
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "@") {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// skillDescription reads the SKILL.md frontmatter description for the skill at
// dir, or "" when absent/unparseable.
func skillDescription(dir string) string {
	skills, err := scanner.Scan(dir)
	if err != nil || len(skills) == 0 {
		return ""
	}
	return skills[0].Description
}

// repoSkillRel returns skill id's repo-relative directory (slash-separated) by
// scanning repoDir, so git operations (add / rm / stat) target the real location
// whether the repo keeps skills at the root or nested under e.g. "skills/". ok is
// false when no skill named id is found in the repo working tree.
func repoSkillRel(repoDir, id string) (string, bool) {
	skills, _ := scanner.Scan(repoDir)
	for _, sk := range skills {
		if sk.LinkName == id || sk.LogicalName == id {
			if rel, err := filepath.Rel(repoDir, sk.Dir); err == nil {
				return filepath.ToSlash(rel), true
			}
		}
	}
	return "", false
}

// syncContrib best-effort records skill → {description, location} in the global
// contribution ledger (清单). It never fails the operation — the ledger is
// metadata for commit summaries and "where did it go", not the skill's source
// of truth. A blank description preserves any prior one.
func (s *Server) syncContrib(skill, description, location string) {
	_ = config.UpsertContrib(s.centralDir, skill, description, location)
}

// pushErrorResponse writes a credential-scrubbed push failure. The local commit
// is intentionally preserved (committed=true) — never --force, never rolled back
// (R5/AE3); U2's commit-aware drift guard keeps it safe until the user retries.
func pushErrorResponse(w http.ResponseWriter, err error) {
	code, detail := "push_failed", err.Error()
	var pe *gitsync.PushError
	if errors.As(err, &pe) {
		code = pe.Code
		if pe.Stderr != "" {
			detail = pe.Stderr
		}
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"error_code": code, "error": gitsync.ScrubCreds(detail), "committed": true,
	})
}

// handleContribute moves a hand-authored skill into a registered git source repo
// (copy → verify → atomic rename, then an in-place symlink), records the
// <repo>/<skill> enabled mapping so reconcile keeps the link, then add+commit
// (MIC-0 …) + push. On push failure the skill stays committed-unpushed and is
// preserved; nothing is force-pushed or rolled back (R1–R5, AE3). CSRF-guarded
// and serialized per repo against the daily/manual sync.
func (s *Server) handleContribute(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		ID          string `json:"id"`
		Root        string `json:"root"`
		Repo        string `json:"repo"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Root = strings.TrimSpace(req.Root)
	req.Repo = strings.TrimSpace(req.Repo)
	if req.ID == "" || req.Root == "" || req.Repo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !reconcile.ValidRepoName(req.Repo) || !safeSkillSeg(req.ID) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}
	wantRoot := harness.Expand(req.Root)

	// Snapshot the bits we need; resolve the root against configured targets and
	// the repo against registered git sources (never trust the raw request to
	// build a path — repo name is validated and re-joined server-side).
	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	canonical := ""
	for _, t := range s.targetsLocked() {
		if harness.Expand(t.Dir) == wantRoot {
			canonical = t.Dir
			break
		}
	}
	s.mu.Unlock()
	_, foundRepo := s.findRepoBranch(req.Repo)

	if canonical == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "未知的来源目录")
		return
	}
	if !foundRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用，无法贡献")
		return
	}
	repoDir := filepath.Join(reposRoot, req.Repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆，请先更新该仓")
		return
	}

	// Per-repo lock for the relocate, so a concurrent sync cannot clean -fd the
	// just-relocated skill before reconcile links it.
	lk := s.repoLock(repoDir)
	lk.Lock()
	defer lk.Unlock()

	// Relocate + link + manifest + enabled, under the config lock (fast, local).
	// This only STAGES the skill in the repo working tree — the push is deferred to
	// the repo card's「更新」(确认 = 更新并上传). 移动/贡献只暂存，不自动推送。
	s.mu.Lock()
	mgr := linker.NewManager(s.reposRoot, s.personalStore)
	if err := adopt.Relocate(req.ID, wantRoot, repoDir, mgr, &s.manifest); err != nil {
		s.mu.Unlock()
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeErr(w, adoptStatus(code), code, err.Error())
		return
	}
	s.cfg.Enabled = ensureEnabled(s.cfg.Enabled, config.EnabledEntry{
		Skill: req.Repo + "/" + req.ID, Target: canonical, Mode: config.ModeSnapshot,
	})
	if err := config.SaveManifest(s.centralDir, s.manifest); err != nil {
		s.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return // persistConfigLocked already wrote the error response
	}
	s.mu.Unlock()

	s.syncContrib(req.ID, sanitizeCommitDesc(req.Description), req.Repo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "staged": true})
}

// handleMoveLocal moves an already-backed-up @local skill into a registered git
// source repo: relocate the store copy into the repo (no in-place store link),
// repoint its enabled mappings from @local/<id> to <repo>/<id>, reconcile so the
// in-place links now resolve to the git source, then add+commit+push. Mirrors
// handleContribute's failure posture: a push failure keeps the skill (now
// committed-unpushed) and reports honestly without rollback. CSRF-guarded and
// per-repo serialized.
func (s *Server) handleMoveLocal(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		ID          string `json:"id"`
		Repo        string `json:"repo"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Repo = strings.TrimSpace(req.Repo)
	if req.ID == "" || req.Repo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !reconcile.ValidRepoName(req.Repo) || !safeSkillSeg(req.ID) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}

	s.mu.Lock()
	reposRoot, personalStore, syncer := s.reposRoot, s.personalStore, s.syncer
	s.mu.Unlock()
	_, foundRepo := s.findRepoBranch(req.Repo)

	if !foundRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用，无法移动")
		return
	}
	repoDir := filepath.Join(reposRoot, req.Repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆，请先更新该仓")
		return
	}
	storeSkill := filepath.Join(harness.Expand(personalStore), req.ID)
	if _, err := os.Stat(filepath.Join(storeSkill, "SKILL.md")); err != nil {
		writeErr(w, http.StatusBadRequest, "not_found", "该 skill 不在本地受管库")
		return
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = skillDescription(storeSkill)
	}

	lk := s.repoLock(repoDir)
	lk.Lock()
	defer lk.Unlock()

	// Move the store copy into the repo (no in-place store link). After this the
	// store slot is gone; reconcile rebuilds the in-place links from the git copy.
	if err := adopt.MoveInto(req.ID, personalStore, repoDir); err != nil {
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeErr(w, adoptStatus(code), code, err.Error())
		return
	}

	// Repoint enabled mappings @local/<id> → <repo>/<id> (preserve target+mode),
	// de-duplicating in case the git selector already existed for a target.
	s.mu.Lock()
	oldSel := reconcile.LocalNamespace + "/" + req.ID
	newSel := req.Repo + "/" + req.ID
	seen := map[string]bool{}
	kept := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if e.Skill == oldSel {
			e.Skill = newSel
		}
		if e.Skill == newSel {
			key := e.Skill + "\x00" + harness.Expand(e.Target)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		kept = append(kept, e)
	}
	s.cfg.Enabled = kept
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Reconcile so the in-place links resolve to the git source (drops the
	// vacated @local links, builds the <repo>/<id> links by rescanning the repo).
	// The skill is now STAGED in the repo working tree; the push is deferred to the
	// repo card's「更新」. 移动只暂存，不自动推送。
	s.ReconcileOnly()
	s.syncContrib(req.ID, sanitizeCommitDesc(desc), req.Repo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "staged": true})
}

// findRepoBranch looks up a registered git source by its on-disk RepoName,
// returning its configured branch and whether it was found. It takes s.mu
// itself, so the caller must NOT already hold it. Callers that only need
// presence ignore the branch.
func (s *Server) findRepoBranch(repo string) (branch string, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rc := range s.cfg.Repos {
		if reconcile.RepoName(rc.URL) == repo {
			return rc.Branch, true
		}
	}
	return "", false
}

// lockRepos acquires the per-repo locks for one or two repo dirs in a stable
// (sorted) order so two concurrent moves can never deadlock. Returns an unlock
// func. A blank or duplicate second dir locks only the first.
func (s *Server) lockRepos(a, b string) func() {
	if b == "" || a == b {
		lk := s.repoLock(a)
		lk.Lock()
		return lk.Unlock
	}
	if a > b {
		a, b = b, a
	}
	la, lb := s.repoLock(a), s.repoLock(b)
	la.Lock()
	lb.Lock()
	return func() { lb.Unlock(); la.Unlock() }
}

// handleMove moves a skill that currently lives in a git repo to another git
// repo or back to the @local store. Policy: target-first, source-cleanup-after
// (see the plan) — copy into the destination and (for a git target) push it
// before touching the source, so no push failure can lose the skill. Enabled
// mappings repoint to the destination; the source copy is then git-removed and
// pushed. CSRF-guarded and locked on both repos.
func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		Name        string `json:"name"`
		FromRepo    string `json:"fromRepo"`
		ToRepo      string `json:"toRepo"` // a git repo name, or "local"
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.FromRepo = strings.TrimSpace(req.FromRepo)
	req.ToRepo = strings.TrimSpace(req.ToRepo)
	toLocal := req.ToRepo == "local"
	if req.Name == "" || req.FromRepo == "" || req.ToRepo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !safeSkillSeg(req.Name) || !reconcile.ValidRepoName(req.FromRepo) || (!toLocal && !reconcile.ValidRepoName(req.ToRepo)) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}
	if req.FromRepo == req.ToRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "源与目标相同")
		return
	}

	s.mu.Lock()
	reposRoot, personalStore, syncer := s.reposRoot, s.personalStore, s.syncer
	s.mu.Unlock()
	_, fromFound := s.findRepoBranch(req.FromRepo)
	_, toFound := s.findRepoBranch(req.ToRepo)

	if !fromFound || (!toLocal && !toFound) {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的源仓或目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用，无法移动")
		return
	}
	fromDir := filepath.Join(reposRoot, req.FromRepo)
	if _, err := os.Stat(filepath.Join(fromDir, ".git")); err != nil {
		writeErr(w, http.StatusConflict, "repo_missing", "源仓尚未克隆")
		return
	}
	// Resolve the skill's real in-repo path up front (it may be nested under
	// skills/); every later git op on the source uses this rel path.
	srcRel, srcOK := repoSkillRel(fromDir, req.Name)
	if !srcOK {
		writeErr(w, http.StatusBadRequest, "not_found", "该 skill 不在源仓")
		return
	}
	destRoot := harness.Expand(personalStore)
	var toDir string
	if !toLocal {
		toDir = filepath.Join(reposRoot, req.ToRepo)
		destRoot = toDir
		if _, err := os.Stat(filepath.Join(toDir, ".git")); err != nil {
			writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆")
			return
		}
	}

	// Description: request → ledger → source SKILL.md.
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		if cm, err := config.LoadContribManifest(s.centralDir); err == nil {
			desc = cm.Skills[req.Name].Description
		}
	}
	if desc == "" {
		desc = skillDescription(filepath.Join(fromDir, filepath.FromSlash(srcRel)))
	}
	unlock := s.lockRepos(fromDir, toDir)
	defer unlock()

	// 1. Copy into destination, KEEPING the source (durable-dest-first). This only
	//    STAGES the skill in the destination working tree — no commit/push here;
	//    the push is deferred to each repo's「更新」. 移动只暂存，不自动推送。
	if err := adopt.CopyInto(req.Name, fromDir, destRoot); err != nil {
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeErr(w, adoptStatus(code), code, err.Error())
		return
	}

	// 2. Destination holds the copy → repoint enabled mappings and reconcile so the
	//    in-place links now resolve to the destination.
	newSel := req.ToRepo + "/" + req.Name
	if toLocal {
		newSel = reconcile.LocalNamespace + "/" + req.Name
	}
	oldSel := req.FromRepo + "/" + req.Name
	s.mu.Lock()
	seen := map[string]bool{}
	kept := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if e.Skill == oldSel {
			e.Skill = newSel
		}
		if e.Skill == newSel {
			key := e.Skill + "\x00" + harness.Expand(e.Target)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		kept = append(kept, e)
	}
	s.cfg.Enabled = kept
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.ReconcileOnly()
	s.syncContrib(req.Name, sanitizeCommitDesc(desc), req.ToRepo)

	// 3. Remove the source copy — a STAGED deletion in the source repo (no push).
	//    The skill is already durable in the destination and links point there; the
	//    source repo now shows a pending deletion to push via its「更新」. Both the
	//    destination's new skill and the source's deletion are local until uploaded.
	if err := os.RemoveAll(filepath.Join(fromDir, filepath.FromSlash(srcRel))); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "staged": true, "warning": "目标已就绪，但源仓删除失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "staged": true})
}

// handleQuickUpload commits (if the working tree is dirty) and pushes a single
// already-in-repo skill's local changes — including the push-retry case where a
// prior contribution committed but failed to push (working tree clean, HEAD
// ahead): then it re-pushes the existing commit without making a new one (R6,
// R7). CSRF-guarded and per-repo serialized.
func (s *Server) handleQuickUpload(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		Repo  string `json:"repo"`
		Skill string `json:"skill"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Skill = strings.TrimSpace(req.Skill)
	if req.Repo == "" || req.Skill == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !reconcile.ValidRepoName(req.Repo) || !safeSkillSeg(req.Skill) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}

	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	s.mu.Unlock()
	branchCfg, foundRepo := s.findRepoBranch(req.Repo)

	if !foundRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用")
		return
	}
	repoDir := filepath.Join(reposRoot, req.Repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆")
		return
	}
	// Resolve the skill's real in-repo path (may be nested under skills/). A
	// deleted-but-pending skill won't scan; fall back to the bare name so the
	// committed-unpushed re-push path still works.
	skillRel, relOK := repoSkillRel(repoDir, req.Skill)
	if !relOK {
		skillRel = req.Skill
	}
	skillDir := filepath.Join(repoDir, filepath.FromSlash(skillRel))

	msg := commitMessage(req.Skill, skillDescription(skillDir))
	ctx := s.detachedCtx()

	lk := s.repoLock(repoDir)
	lk.Lock()
	defer lk.Unlock()

	branch, err := syncer.ResolveBranch(ctx, repoDir, branchCfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "branch_failed", err.Error())
		return
	}

	kind, dirty := syncer.Drift(ctx, repoDir, branch).Has(req.Skill)
	if !dirty {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nothing": true})
		return
	}
	// Working-tree changes (new/modified/deleted) need a fresh commit; a
	// committed-unpushed skill only needs the existing commit re-pushed.
	if kind == gitsync.DriftAdded || kind == gitsync.DriftModified || kind == gitsync.DriftDeleted {
		if err := syncer.Add(ctx, repoDir, skillRel); err != nil {
			writeErr(w, http.StatusInternalServerError, "add_failed", err.Error())
			return
		}
		if _, err := syncer.Commit(ctx, repoDir, msg); err != nil {
			writeErr(w, http.StatusInternalServerError, "commit_failed", err.Error())
			return
		}
	}
	if err := syncer.Push(ctx, repoDir, branch); err != nil {
		pushErrorResponse(w, err)
		return
	}
	_ = syncer.AlignWorkingTree(ctx, repoDir, branch)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "commit": msg})
}

// handleRepoDrift returns the changed (unpushed) skills of one git repo so the
// repo popup's「上传」dialog can list exactly what is uploadable. Purely local
// (no fetch); read-only GET. Root-level changes (skill == "") are dropped — the
// dialog uploads per-skill. Each entry carries kind so the UI can label it.
func (s *Server) handleRepoDrift(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" || !reconcile.ValidRepoName(repo) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名")
		return
	}
	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	s.mu.Unlock()
	branchCfg, foundRepo := s.findRepoBranch(repo)
	if !foundRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用")
		return
	}
	repoDir := filepath.Join(reposRoot, repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆")
		return
	}
	ctx := s.detachedCtx()
	branch, err := syncer.ResolveBranch(ctx, repoDir, branchCfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "branch_failed", err.Error())
		return
	}
	type driftItem struct {
		Skill string            `json:"skill"` // display name
		Path  string            `json:"path"`  // repo-relative dir (may be nested); what /api/upload selects by
		Kind  gitsync.DriftKind `json:"kind"`
	}
	out := []driftItem{}
	for _, e := range syncer.Drift(ctx, repoDir, branch).Entries {
		if e.Skill == "" || e.Path == "" {
			continue // repo-root file not attributable to a skill
		}
		out = append(out, driftItem{Skill: e.Skill, Path: e.Path, Kind: e.Kind})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": out})
}

// handleSkillAuthorship returns a git-source skill's creator + last editor (with
// short dates), derived from git log on the skill's real in-repo path. Read-only
// GET; only meaningful for git repos (local / skills.sh have no git history).
func (s *Server) handleSkillAuthorship(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	skill := strings.TrimSpace(r.URL.Query().Get("skill"))
	if repo == "" || skill == "" || !reconcile.ValidRepoName(repo) || !safeSkillSeg(skill) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}
	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	s.mu.Unlock()
	_, found := s.findRepoBranch(repo)
	if !found {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用")
		return
	}
	repoDir := filepath.Join(reposRoot, repo)
	rel, ok := repoSkillRel(repoDir, skill)
	if !ok {
		writeErr(w, http.StatusBadRequest, "not_found", "该 skill 不在仓内")
		return
	}
	a := syncer.Authorship(s.detachedCtx(), repoDir, rel)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "authorship": a})
}

// handleRepoCreators returns every skill's creator in one git-source repo (keyed
// by skill directory name), for the per-card creator badge. One git-log pass.
func (s *Server) handleRepoCreators(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" || !reconcile.ValidRepoName(repo) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名")
		return
	}
	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	s.mu.Unlock()
	_, found := s.findRepoBranch(repo)
	if !found || syncer == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "creators": map[string]any{}})
		return
	}
	repoDir := filepath.Join(reposRoot, repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "creators": map[string]any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "creators": syncer.RepoCreators(s.detachedCtx(), repoDir)})
}

// handleUpload commits + pushes a user-selected set of changed skills in one
// repo with an editable commit message — the repo-popup「上传」action (the
// per-skill 快捷上传 collapsed into multi-select + custom message). Each selected
// skill with working-tree changes (added/modified) is staged; one commit with the
// supplied message is made; a single push then sends all pending commits on the
// branch. committed-unpushed skills need no fresh staging. On push failure the
// commit is preserved (committed=true), never force-pushed (R5/AE3). CSRF-guarded,
// serialized per repo against the daily/manual sync.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		Repo    string   `json:"repo"`
		Skills  []string `json:"skills"` // repo-relative skill paths (from /api/repo-drift)
		Message string   `json:"message"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Repo = strings.TrimSpace(req.Repo)
	if req.Repo == "" || len(req.Skills) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !reconcile.ValidRepoName(req.Repo) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名")
		return
	}
	// The requested entries are skill PATHS; they are only honored if they match a
	// live drift entry below (authoritative), so no path validation is needed here.
	want := map[string]bool{}
	for _, p := range req.Skills {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	if len(want) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	s.mu.Unlock()
	branchCfg, foundRepo := s.findRepoBranch(req.Repo)

	if !foundRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	if syncer == nil {
		writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用")
		return
	}
	repoDir := filepath.Join(reposRoot, req.Repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆")
		return
	}

	ctx := s.detachedCtx()
	lk := s.repoLock(repoDir)
	lk.Lock()
	defer lk.Unlock()

	branch, err := syncer.ResolveBranch(ctx, repoDir, branchCfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "branch_failed", err.Error())
		return
	}

	// Only act on requested paths that match a live drift entry (authoritative).
	// Working-tree changes (added/modified/deleted) are staged; a committed-unpushed
	// skill needs no fresh staging — the existing commit is re-pushed.
	drift := syncer.Drift(ctx, repoDir, branch)
	var toAdd, pending, names []string
	for _, e := range drift.Entries {
		if e.Path == "" || !want[e.Path] {
			continue
		}
		pending = append(pending, e.Path)
		names = append(names, e.Skill)
		if e.Kind == gitsync.DriftAdded || e.Kind == gitsync.DriftModified || e.Kind == gitsync.DriftDeleted {
			toAdd = append(toAdd, e.Path)
		}
	}
	if len(pending) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nothing": true})
		return
	}

	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		msg = commitTicket + " 更新 " + strings.Join(names, ", ")
	} else if rs := []rune(msg); len(rs) > 2000 {
		msg = strings.TrimSpace(string(rs[:2000]))
	}

	if len(toAdd) > 0 {
		if err := syncer.Add(ctx, repoDir, toAdd...); err != nil {
			writeErr(w, http.StatusInternalServerError, "add_failed", err.Error())
			return
		}
		if _, err := syncer.Commit(ctx, repoDir, msg); err != nil {
			writeErr(w, http.StatusInternalServerError, "commit_failed", err.Error())
			return
		}
	}
	if err := syncer.Push(ctx, repoDir, branch); err != nil {
		pushErrorResponse(w, err)
		return
	}
	_ = syncer.AlignWorkingTree(ctx, repoDir, branch)
	for i, p := range pending {
		s.syncContrib(names[i], skillDescription(filepath.Join(repoDir, filepath.FromSlash(p))), req.Repo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "commit": msg, "uploaded": names})
}

// handleDeleteRepoSkill removes a single skill from a git-source repo's working
// tree (resolving its real, possibly-nested path) and tears down its enabled
// links. It does NOT commit or push: the removal becomes a pending deletion the
// user pushes via the repo「上传」dialog (deletion is just another local change),
// so the remote — the source of truth — only changes on an explicit upload. The
// commit-aware drift guard protects the pending deletion from a reset-on-update
// in the meantime. CSRF-guarded and per-repo serialized.
func (s *Server) handleDeleteRepoSkill(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		Repo  string `json:"repo"`
		Skill string `json:"skill"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Skill = strings.TrimSpace(req.Skill)
	if req.Repo == "" || req.Skill == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !reconcile.ValidRepoName(req.Repo) || !safeSkillSeg(req.Skill) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}

	s.mu.Lock()
	reposRoot := s.reposRoot
	foundRepo := false
	for _, rc := range s.cfg.Repos {
		if reconcile.RepoName(rc.URL) == req.Repo {
			foundRepo = true
			break
		}
	}
	if !foundRepo {
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
		return
	}
	// Drop enabled selections for this skill so reconcile removes its links.
	sel := req.Repo + "/" + req.Skill
	kept := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if e.Skill != sel {
			kept = append(kept, e)
		}
	}
	s.cfg.Enabled = kept
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	repoDir := filepath.Join(reposRoot, req.Repo)
	skillRel, ok := repoSkillRel(repoDir, req.Skill)
	if !ok {
		writeErr(w, http.StatusBadRequest, "not_found", "该 skill 不在仓内")
		return
	}
	lk := s.repoLock(repoDir)
	lk.Lock()
	err := os.RemoveAll(filepath.Join(repoDir, filepath.FromSlash(skillRel)))
	lk.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	s.ReconcileOnly() // tear down the links to the now-removed skill
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
