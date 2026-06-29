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

// maxCommitDescRunes bounds the editable commit summary so a pasted multi-page
// description cannot bloat the commit subject.
const maxCommitDescRunes = 200

// commitMessage builds "<skill> <description>", falling back to "<skill>" when
// the (sanitized) description is empty.
func commitMessage(skill, desc string) string {
	d := sanitizeCommitDesc(desc)
	if d == "" {
		return skill
	}
	return skill + " " + d
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
// (commit) + push. On push failure the skill stays committed-unpushed and is
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

// handleMoveStore moves a skill that lives in a store-like source — the @local
// managed store or a registered "@dir:<id>" folder — to any move target: @local,
// another registered folder, or a git repo. It relocates the body into the
// destination (MoveInto: copy → verify → remove the source slot), repoints the
// enabled mappings <srcNS>/<id> → <dstNS or repo>/<id>, then reconciles so the
// in-place links resolve to the new home. A git target additionally stages a
// deferred push (handleContribute's failure posture: a push failure keeps the
// skill committed-unpushed, never rolled back). 移动只暂存，不自动推送。
//
// Back-compat: the legacy {id, repo} body (the old @local→git move) is accepted
// by defaulting From to @local and To to Repo. CSRF-guarded; per-repo serialized
// when the target is git.
func (s *Server) handleMoveStore(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		ID          string `json:"id"`
		From        string `json:"from"` // "@local"/"local" or "@dir:<id>"
		To          string `json:"to"`   // "local", "@dir:<id>", or a git repo name
		Repo        string `json:"repo"` // legacy alias for To (@local → git)
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.From = strings.TrimSpace(req.From)
	req.To = strings.TrimSpace(req.To)
	req.Repo = strings.TrimSpace(req.Repo)
	if req.From == "" {
		req.From = reconcile.LocalNamespace // legacy {id, repo} meant @local → repo
	}
	if req.To == "" {
		req.To = req.Repo
	}
	if req.ID == "" || req.To == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !safeSkillSeg(req.ID) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的 skill 名")
		return
	}

	srcRoot, srcNS, srcOK, smsg := s.resolveStoreSource(req.From)
	if !srcOK {
		writeErr(w, http.StatusBadRequest, "invalid", smsg)
		return
	}
	destRoot, destSelNS, destLocalLike, derr := s.resolveLocalLikeDest(req.To)
	if destLocalLike && derr != "" {
		writeErr(w, http.StatusBadRequest, "invalid", derr)
		return
	}
	newSel := req.To + "/" + req.ID
	if destLocalLike {
		newSel = destSelNS + "/" + req.ID
	}
	oldSel := srcNS + "/" + req.ID
	if oldSel == newSel {
		writeErr(w, http.StatusBadRequest, "invalid", "源与目标相同")
		return
	}

	// The source skill must exist in the source root. Refuse moving OUT a skill
	// that IS the registered folder root itself — that would empty/destroy the
	// user's registered source folder (suggest unregistering instead).
	srcSkillDir := ""
	for _, sk := range scanSourceSkills(srcRoot) {
		if sk.LinkName == req.ID || sk.LogicalName == req.ID {
			srcSkillDir = sk.Dir
			break
		}
	}
	if srcSkillDir == "" {
		writeErr(w, http.StatusBadRequest, "not_found", "该 skill 不在来源")
		return
	}
	if strings.HasPrefix(srcNS, reconcile.DirNamespacePrefix) && harness.Expand(srcSkillDir) == harness.Expand(srcRoot) {
		writeErr(w, http.StatusBadRequest, "invalid", "该本地源是单个 skill 文件夹，移出会清空它；如要撤销请用侧栏「移除」该源")
		return
	}

	// A git target must be a cloned registered repo; lock it for the relocate +
	// deferred push. Local-like targets need no git.
	var repoDir string
	if !destLocalLike {
		if _, found := s.findRepoBranch(req.To); !found {
			writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
			return
		}
		s.mu.Lock()
		reposRoot, syncer := s.reposRoot, s.syncer
		s.mu.Unlock()
		if syncer == nil {
			writeErr(w, http.StatusPreconditionFailed, "no_git", "git 不可用，无法移动")
			return
		}
		repoDir = filepath.Join(reposRoot, req.To)
		destRoot = repoDir
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
			writeErr(w, http.StatusConflict, "repo_missing", "目标仓尚未克隆，请先更新该仓")
			return
		}
		lk := s.repoLock(repoDir)
		lk.Lock()
		defer lk.Unlock()
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = skillDescription(srcSkillDir)
	}

	// Move the body into the destination (no in-place source link). After this the
	// source slot is gone; reconcile rebuilds the in-place links from the new home.
	if err := adopt.MoveInto(req.ID, srcRoot, destRoot); err != nil {
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeErr(w, adoptStatus(code), code, err.Error())
		return
	}

	// Repoint enabled mappings <srcNS>/<id> → <dstNS or repo>/<id> (preserve
	// target+mode), de-duplicating in case the destination selector already existed.
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
	s.syncContrib(req.ID, sanitizeCommitDesc(desc), req.To)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "staged": true})
}

// scanSourceSkills scans a store-like source root for its skills, tolerating a
// missing/unreadable root (returns nil). Thin wrapper so the move handler reads
// cleanly.
func scanSourceSkills(root string) []scanner.Skill {
	skills, _ := scanner.Scan(harness.Expand(root))
	return skills
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

// resolveLocalLikeDest resolves a non-git move destination — the @local managed
// store ("local") or a registered local folder source ("@dir:<id>") — to its
// on-disk root and selector namespace. localLike=false means `to` is not a
// local-like target and the caller should treat it as a git repo name. When
// localLike=true and msg!="" the destination was local-like but invalid (e.g.
// an unregistered @dir id) and the caller must reject with msg. Takes s.mu
// itself, so the caller must NOT already hold it.
func (s *Server) resolveLocalLikeDest(to string) (destRoot, selNS string, localLike bool, msg string) {
	if to == "local" {
		s.mu.Lock()
		ps := s.personalStore
		s.mu.Unlock()
		return harness.Expand(ps), reconcile.LocalNamespace, true, ""
	}
	if id, ok := strings.CutPrefix(to, reconcile.DirNamespacePrefix); ok {
		s.mu.Lock()
		path := s.dirSourceMapLocked()[id]
		s.mu.Unlock()
		if path == "" {
			return "", "", true, "未登记的本地源"
		}
		return path, reconcile.DirSelector(id), true, ""
	}
	return "", "", false, ""
}

// resolveStoreSource resolves a store-like move source — the @local managed
// store ("@local"/"local") or a registered local folder source ("@dir:<id>") —
// to its on-disk root and selector namespace. Takes s.mu itself, so the caller
// must NOT already hold it.
func (s *Server) resolveStoreSource(from string) (srcRoot, srcNS string, ok bool, msg string) {
	if from == "local" || from == reconcile.LocalNamespace {
		s.mu.Lock()
		ps := s.personalStore
		s.mu.Unlock()
		return harness.Expand(ps), reconcile.LocalNamespace, true, ""
	}
	if id, isDir := strings.CutPrefix(from, reconcile.DirNamespacePrefix); isDir {
		s.mu.Lock()
		path := s.dirSourceMapLocked()[id]
		s.mu.Unlock()
		if path == "" {
			return "", "", false, "未登记的本地源"
		}
		return path, reconcile.DirSelector(id), true, ""
	}
	return "", "", false, "非法的来源"
}

// handleMove moves a skill that currently lives in a git repo to a local-like
// store (@local / a registered "@dir:<id>" folder) or another git repo.
// Policy: target-first, source-cleanup-after
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
	if req.Name == "" || req.FromRepo == "" || req.ToRepo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The destination is either a local-like store (@local "local" / a registered
	// "@dir:<id>" folder) or another git repo. Local-like targets relocate the
	// body + repoint the selector with no push; a git target stages a push.
	destRoot, destSelNS, destLocalLike, derr := s.resolveLocalLikeDest(req.ToRepo)
	if destLocalLike && derr != "" {
		writeErr(w, http.StatusBadRequest, "invalid", derr)
		return
	}
	if !safeSkillSeg(req.Name) || !reconcile.ValidRepoName(req.FromRepo) || (!destLocalLike && !reconcile.ValidRepoName(req.ToRepo)) {
		writeErr(w, http.StatusBadRequest, "invalid", "非法的仓名或 skill 名")
		return
	}
	if req.FromRepo == req.ToRepo {
		writeErr(w, http.StatusBadRequest, "invalid", "源与目标相同")
		return
	}

	s.mu.Lock()
	reposRoot, syncer := s.reposRoot, s.syncer
	s.mu.Unlock()
	if _, fromFound := s.findRepoBranch(req.FromRepo); !fromFound {
		writeErr(w, http.StatusBadRequest, "invalid", "未登记的源仓")
		return
	}
	if !destLocalLike {
		if _, toFound := s.findRepoBranch(req.ToRepo); !toFound {
			writeErr(w, http.StatusBadRequest, "invalid", "未登记的目标仓")
			return
		}
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
	var toDir string
	if !destLocalLike {
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
	if destLocalLike {
		newSel = destSelNS + "/" + req.Name
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

	drift := syncer.Drift(ctx, repoDir, branch)
	kind, dirty := drift.Has(req.Skill)
	if !dirty {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nothing": true})
		return
	}
	// Secret guard: the quick action has no confirm step, so refuse outright if
	// this skill carries a credential/key-looking file and route the user to the
	// repo「同步仓库」dialog, which can confirm-and-push explicitly.
	if secs := drift.SecretsUnder(skillRel); len(secs) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error_code": "secrets_blocked",
			"error":   "该 skill 含疑似密钥/凭据文件，快捷上传已拦截，请用「同步仓库」确认后再推",
			"secrets": secs,
		})
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
	drift := syncer.Drift(ctx, repoDir, branch)
	out := []driftItem{}
	for _, e := range drift.Entries {
		if e.Skill == "" || e.Path == "" {
			continue // repo-root file not attributable to a skill
		}
		out = append(out, driftItem{Skill: e.Skill, Path: e.Path, Kind: e.Kind})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": out, "secrets": drift.Secrets})
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
		Repo           string   `json:"repo"`
		Skills         []string `json:"skills"` // repo-relative skill paths (from /api/repo-drift)
		Message        string   `json:"message"`
		ConfirmSecrets bool     `json:"confirmSecrets"` // user explicitly acknowledged pushing secret-looking files
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
	// Secret guard (server-side enforcement, not just the UI checkbox): refuse to
	// push credential/key-looking files unless the caller explicitly confirmed.
	// Returns the offending paths so the dialog can list them. Upload is
	// all-or-nothing, so any flagged secret in the drift would be pushed.
	if len(drift.Secrets) > 0 && !req.ConfirmSecrets {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error_code": "secrets_blocked",
			"error":   "检测到疑似密钥/凭据文件，未确认不予推送",
			"secrets": drift.Secrets,
		})
		return
	}
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
		msg = "更新 " + strings.Join(names, ", ")
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
