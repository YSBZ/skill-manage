package server

import (
	"encoding/json"
	"errors"
	"fmt"
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

// Handler builds the full HTTP handler: token-authed /api routes plus the
// host-guarded SPA.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("POST /api/repos", s.requireAuth(s.handleAddRepo))
	mux.HandleFunc("DELETE /api/repos", s.requireAuth(s.handleRemoveRepo))
	mux.HandleFunc("GET /api/repos/export", s.requireAuth(s.handleExportRepos))
	mux.HandleFunc("POST /api/repos/import", s.requireAuth(s.handleImportRepos))
	mux.HandleFunc("GET /api/targets", s.requireAuth(s.handleTargets))
	mux.HandleFunc("GET /api/skills", s.requireAuth(s.handleListSkills))
	mux.HandleFunc("GET /api/skill", s.requireAuth(s.handleSkillDetail))
	mux.HandleFunc("GET /api/adoptable", s.requireAuth(s.handleAdoptable))
	mux.HandleFunc("POST /api/adopt", s.requireAuth(s.handleAdopt))
	mux.HandleFunc("POST /api/enabled", s.requireAuth(s.handleAddEnabled))
	mux.HandleFunc("DELETE /api/enabled", s.requireAuth(s.handleRemoveEnabled))
	mux.HandleFunc("POST /api/targets", s.requireAuth(s.handleAddTarget))
	mux.HandleFunc("DELETE /api/targets", s.requireAuth(s.handleRemoveTarget))
	mux.HandleFunc("POST /api/update-now", s.requireAuth(s.handleUpdateNow))
	mux.HandleFunc("POST /api/apply", s.requireAuth(s.handleApply))
	mux.HandleFunc("GET /api/autostart", s.requireAuth(s.handleAutostartStatus))
	mux.HandleFunc("POST /api/autostart", s.requireAuth(s.handleAutostartSet))

	mux.HandleFunc("/", s.hostGuard(s.spaHandler()))
	return mux
}

// --- autostart ---

func (s *Server) handleAutostartStatus(w http.ResponseWriter, r *http.Request) {
	if s.autostart == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"supported": false, "registered": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"supported": true, "registered": s.autostart.IsRegistered()})
}

type autostartReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleAutostartSet(w http.ResponseWriter, r *http.Request) {
	if s.autostart == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "autostart not supported on this platform"})
		return
	}
	var req autostartReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	if req.Enabled {
		err = s.autostart.Register()
	} else {
		err = s.autostart.Unregister()
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"registered": s.autostart.IsRegistered()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// --- status ---

type statusResp struct {
	FirstRun    bool                  `json:"firstRun"`
	Repos       []RepoStatus          `json:"repos"`
	Enabled     []config.EnabledEntry `json:"enabled"`
	Targets     []string              `json:"targets"`
	Links       []config.LinkRecord   `json:"links"`
	LastSummary reconcile.Summary     `json:"lastSummary"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := statusResp{
		FirstRun:    s.firstRun,
		Enabled:     s.cfg.Enabled,
		Targets:     s.cfg.Targets,
		Links:       s.manifest.Links,
		LastSummary: s.lastSummary,
	}
	// repo status in config order
	for _, repo := range s.cfg.Repos {
		if st, ok := s.repoStatus[repo.URL]; ok {
			resp.Repos = append(resp.Repos, st)
		} else {
			resp.Repos = append(resp.Repos, RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: reconcile.RepoName(repo.URL), State: "never-synced"})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- repos ---

type repoReq struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
}

func (s *Server) handleAddRepo(w http.ResponseWriter, r *http.Request) {
	var req repoReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := gitsync.ValidateRepoURL(req.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !reconcile.ValidRepoName(reconcile.RepoName(req.URL)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("repo directory name %q is not allowed (reserved prefix or illegal characters)", reconcile.RepoName(req.URL)),
		})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existingURLs := make([]string, 0, len(s.cfg.Repos))
	for _, existing := range s.cfg.Repos {
		if existing.URL == req.URL {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "repo already tracked"})
			return
		}
		existingURLs = append(existingURLs, existing.URL)
	}
	if reconcile.RepoNameCollides(existingURLs, req.URL) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("repo name %q collides with an already-tracked repo (same directory name); rename one upstream or track a different fork", reconcile.RepoName(req.URL)),
		})
		return
	}
	s.cfg.Repos = append(s.cfg.Repos, config.RepoConfig{URL: req.URL, Branch: req.Branch})
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": reconcile.RepoName(req.URL)})
}

func (s *Server) handleRemoveRepo(w http.ResponseWriter, r *http.Request) {
	var req repoReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cfg.Repos[:0]
	for _, repo := range s.cfg.Repos {
		if repo.URL != req.URL {
			out = append(out, repo)
		}
	}
	s.cfg.Repos = out
	delete(s.repoStatus, req.URL)
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleExportRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.cfg.Repos)
}

type importReq struct {
	Repos []repoReq `json:"repos"`
}

func (s *Server) handleImportRepos(w http.ResponseWriter, r *http.Request) {
	var req importReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Validate all first; reject the whole import on any invalid entry (plan U10).
	var rejected []map[string]string
	for _, rp := range req.Repos {
		if err := gitsync.ValidateRepoURL(rp.URL); err != nil {
			rejected = append(rejected, map[string]string{"url": rp.URL, "error": err.Error()})
			continue
		}
		if !reconcile.ValidRepoName(reconcile.RepoName(rp.URL)) {
			rejected = append(rejected, map[string]string{"url": rp.URL, "error": "illegal repo directory name (reserved prefix or characters)"})
		}
	}
	if len(rejected) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"rejected": rejected})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	have := map[string]bool{}
	// nameOwner maps each on-disk RepoName to the URL that owns it, seeded from
	// already-tracked repos. A second URL claiming the same name (whether from
	// the existing set or earlier in this batch) is a collision (KTD5/R5): the
	// two would share one mirror dir. Reject the whole import so the user fixes
	// the source list rather than silently dropping one repo.
	nameOwner := map[string]string{}
	for _, repo := range s.cfg.Repos {
		have[repo.URL] = true
		nameOwner[reconcile.RepoName(repo.URL)] = repo.URL
	}
	for _, rp := range req.Repos {
		if have[rp.URL] {
			continue // duplicate URL is skipped below, not a collision
		}
		name := reconcile.RepoName(rp.URL)
		if owner, taken := nameOwner[name]; taken && owner != rp.URL {
			rejected = append(rejected, map[string]string{
				"url":   rp.URL,
				"error": fmt.Sprintf("repo name %q collides with %q (same directory name)", name, owner),
			})
			continue
		}
		nameOwner[name] = rp.URL // claim it so a later batch entry collides too
	}
	if len(rejected) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"rejected": rejected})
		return
	}
	added, skipped := 0, 0
	for _, rp := range req.Repos {
		if have[rp.URL] {
			skipped++
			continue
		}
		s.cfg.Repos = append(s.cfg.Repos, config.RepoConfig{URL: rp.URL, Branch: rp.Branch})
		have[rp.URL] = true
		nameOwner[reconcile.RepoName(rp.URL)] = rp.URL
		added++
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": added, "skipped": skipped})
}

// --- targets ---

// handleTargets returns the user's sync directories, each classified by the
// agent that consumes it (cc vs codex). No personal/project split — the list is
// exactly what the user configured.
func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	targets := s.targetsLocked()
	for i := range targets {
		targets[i].Alias = s.cfg.TargetAliases[targets[i].Dir]
	}
	s.mu.Unlock()
	if targets == nil {
		targets = []harness.Target{}
	}
	writeJSON(w, http.StatusOK, targets)
}

// targetsLocked classifies the configured sync dirs. Caller must hold s.mu.
func (s *Server) targetsLocked() []harness.Target {
	return harness.Targets(s.cfg.Targets)
}

// --- skills ---

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	root, ok := s.sourceRoot(r.URL.Query().Get("repo"))
	if !ok {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	skills, err := scanner.Scan(root)
	if err != nil {
		writeJSON(w, http.StatusOK, []scanner.Skill{}) // not yet synced → empty
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// sourceRoot resolves a source selector to its on-disk root: the reserved
// "@local" namespace → the personal (adopted-skill) store; any other valid repo
// name → reposRoot/<name>. Mirrors reconcile.sourceRoot so the UI can list both
// tracked-repo skills and adopted @local skills through one endpoint.
func (s *Server) sourceRoot(repo string) (string, bool) {
	if repo == reconcile.LocalNamespace {
		return s.personalStore, true
	}
	if reconcile.ValidRepoName(repo) {
		return filepath.Join(s.reposRoot, repo), true
	}
	return "", false
}

// skillDetail is the full view of one skill (its SKILL.md content + metadata).
type skillDetail struct {
	LinkName    string `json:"linkName"`
	LogicalName string `json:"logicalName"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

func (s *Server) handleSkillDetail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	root, ok := s.sourceRoot(r.URL.Query().Get("repo"))
	if !ok || !reconcile.ValidRepoName(name) {
		http.Error(w, "invalid repo or name", http.StatusBadRequest)
		return
	}
	skills, err := scanner.Scan(root)
	if err != nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	for _, sk := range skills {
		if sk.LinkName == name || sk.LogicalName == name {
			content, _ := os.ReadFile(filepath.Join(sk.Dir, "SKILL.md"))
			writeJSON(w, http.StatusOK, skillDetail{
				LinkName:    sk.LinkName,
				LogicalName: sk.LogicalName,
				Description: sk.Description,
				Content:     string(content),
			})
			return
		}
	}
	http.Error(w, "skill not found", http.StatusNotFound)
}

// --- adopt (个人 skill 反向管理 / 收编) ---
//
// Adoption scans and relocates from every configured sync directory — each is a
// bidirectional dir where the user can hand-author skills (KTD1). No
// personal/project split: the source roots are exactly the user's targets.

func (s *Server) handleAdoptable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	snapshot := config.Manifest{Links: append([]config.LinkRecord(nil), s.manifest.Links...)}
	roots := s.targetsLocked()
	s.mu.Unlock()
	list, err := adopt.ListAdoptable(roots, &snapshot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []adopt.Adoptable{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": list})
}

type adoptReq struct {
	ID   string `json:"id"`
	Root string `json:"root"` // absolute source root from /api/adoptable
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	var req adoptReq
	if err := readJSON(r, &req); err != nil || req.ID == "" || req.Root == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The root must be one of the configured sync dirs — never an arbitrary
	// client-supplied path. This bounds adoption to the directories
	// /api/adoptable is allowed to expose.
	wantRoot := harness.Expand(req.Root)
	mgr := linker.NewManager(s.reposRoot, s.personalStore)
	s.mu.Lock()
	defer s.mu.Unlock()
	canonical := ""
	for _, t := range s.targetsLocked() {
		if harness.Expand(t.Dir) == wantRoot {
			canonical = t.Dir // keep the user-facing form (~) for the enabled entry
			break
		}
	}
	if canonical == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error_code": "invalid", "error": "unknown adopt root"})
		return
	}
	if err := adopt.Adopt(req.ID, wantRoot, s.personalStore, mgr, &s.manifest); err != nil {
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeJSON(w, adoptStatus(code), map[string]string{"error_code": code, "error": err.Error()})
		return
	}
	// Record the in-place link as a first-class mapping (@local/<id> → root) so
	// reconcile keeps it. Without this the link is manifest-only and reconcile's
	// orphan pass (no matching enabled entry) tears it down on the next sync —
	// silently un-doing "收编后默认软链回去".
	s.cfg.Enabled = ensureEnabled(s.cfg.Enabled, config.EnabledEntry{
		Skill: reconcile.LocalNamespace + "/" + req.ID, Target: canonical, Mode: config.ModeSnapshot,
	})
	if err := config.SaveManifest(s.centralDir, s.manifest); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error_code": "save_failed", "error": err.Error()})
		return
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ensureEnabled appends e unless an entry with the same Skill and an
// equivalent (expanded) Target already exists. Returns the (possibly unchanged)
// slice.
func ensureEnabled(list []config.EnabledEntry, e config.EnabledEntry) []config.EnabledEntry {
	want := harness.Expand(e.Target)
	for _, x := range list {
		if x.Skill == e.Skill && harness.Expand(x.Target) == want {
			return list
		}
	}
	return append(list, e)
}

func adoptStatus(code string) int {
	switch code {
	case "invalid", "guarded", "not_found":
		return http.StatusBadRequest
	case "name_taken":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// --- enabled ---

func (s *Server) handleAddEnabled(w http.ResponseWriter, r *http.Request) {
	var e config.EnabledEntry
	if err := readJSON(r, &e); err != nil || e.Skill == "" || e.Target == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Never let a selection target a Codex-guarded directory (KTD7 #3): the
	// guard must hold on the write path, not only in the target list.
	if harness.Guarded(e.Target) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is a guarded directory"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.cfg.Enabled {
		if x.Skill == e.Skill && x.Target == e.Target {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) // already enabled
			return
		}
	}
	s.cfg.Enabled = append(s.cfg.Enabled, e)
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveEnabled(w http.ResponseWriter, r *http.Request) {
	var e config.EnabledEntry
	if err := readJSON(r, &e); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cfg.Enabled[:0]
	for _, x := range s.cfg.Enabled {
		if x.Skill == e.Skill && x.Target == e.Target {
			continue
		}
		out = append(out, x)
	}
	s.cfg.Enabled = out
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- sync directories (targets) ---

type targetReq struct {
	Dir   string `json:"dir"`
	Alias string `json:"alias"`
}

func (s *Server) handleAddTarget(w http.ResponseWriter, r *http.Request) {
	var req targetReq
	dir, alias := "", ""
	if err := readJSON(r, &req); err == nil {
		dir = strings.TrimSpace(req.Dir)
		alias = strings.TrimSpace(req.Alias)
	}
	if dir == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// A guarded directory (Codex .system / vendor_imports) must never become a
	// sync target — the guard holds on the write path, not just in listings.
	if harness.Guarded(dir) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "directory is guarded"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.cfg.Targets {
		if d == dir {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "already a sync directory"})
			return
		}
	}
	s.cfg.Targets = append(s.cfg.Targets, dir)
	if alias != "" {
		if s.cfg.TargetAliases == nil {
			s.cfg.TargetAliases = map[string]string{}
		}
		s.cfg.TargetAliases[dir] = alias
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveTarget(w http.ResponseWriter, r *http.Request) {
	var req targetReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dir := strings.TrimSpace(req.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Drop the directory and any enabled selection that pointed at it, so its
	// links are reconciled away on the next cycle (no dangling selections).
	out := s.cfg.Targets[:0]
	for _, d := range s.cfg.Targets {
		if d != dir {
			out = append(out, d)
		}
	}
	s.cfg.Targets = out
	delete(s.cfg.TargetAliases, dir)
	kept := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if e.Target != dir {
			kept = append(kept, e)
		}
	}
	s.cfg.Enabled = kept
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- update-now ---

type updateReq struct {
	Force bool `json:"force"`
}

func (s *Server) handleUpdateNow(w http.ResponseWriter, r *http.Request) {
	var req updateReq
	_ = readJSON(r, &req) // empty body is fine (force defaults false)
	// Use the daemon-lifetime context, not the request's: closing the browser
	// tab mid-sync must not cancel in-flight git subprocesses (and leave repos
	// half-fetched), but daemon shutdown still cancels so the sync drains.
	sum := s.SyncAll(s.detachedCtx(), req.Force)
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ReconcileOnly())
}

// persistConfigLocked saves config; on failure writes a 500 and returns the
// error so the caller stops. Caller must hold s.mu.
func (s *Server) persistConfigLocked(w http.ResponseWriter) error {
	s.firstRun = false
	if err := config.SaveConfig(s.centralDir, s.cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return err
	}
	return nil
}
