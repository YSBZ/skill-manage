package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"

	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
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
	mux.HandleFunc("GET /api/skills", s.requireAuth(s.handleListSkills))
	mux.HandleFunc("POST /api/enabled", s.requireAuth(s.handleAddEnabled))
	mux.HandleFunc("DELETE /api/enabled", s.requireAuth(s.handleRemoveEnabled))
	mux.HandleFunc("POST /api/projects", s.requireAuth(s.handleAddProject))
	mux.HandleFunc("DELETE /api/projects", s.requireAuth(s.handleRemoveProject))
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
	Projects    []string              `json:"projects"`
	Links       []config.LinkRecord   `json:"links"`
	LastSummary reconcile.Summary     `json:"lastSummary"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := statusResp{
		FirstRun:    s.firstRun,
		Enabled:     s.cfg.Enabled,
		Projects:    s.cfg.Projects,
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
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.cfg.Repos {
		if existing.URL == req.URL {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "repo already tracked"})
			return
		}
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
		}
	}
	if len(rejected) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"rejected": rejected})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	have := map[string]bool{}
	for _, repo := range s.cfg.Repos {
		have[repo.URL] = true
	}
	added, skipped := 0, 0
	for _, rp := range req.Repos {
		if have[rp.URL] {
			skipped++
			continue
		}
		s.cfg.Repos = append(s.cfg.Repos, config.RepoConfig{URL: rp.URL, Branch: rp.Branch})
		have[rp.URL] = true
		added++
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": added, "skipped": skipped})
}

// --- skills ---

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if !reconcile.ValidRepoName(repo) {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	skills, err := scanner.Scan(filepath.Join(s.reposRoot, repo))
	if err != nil {
		writeJSON(w, http.StatusOK, []scanner.Skill{}) // not yet synced → empty
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// --- enabled ---

func (s *Server) handleAddEnabled(w http.ResponseWriter, r *http.Request) {
	var e config.EnabledEntry
	if err := readJSON(r, &e); err != nil || e.Skill == "" || e.Target == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
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

// --- projects ---

type projectReq struct {
	Path string `json:"path"`
}

func (s *Server) handleAddProject(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.cfg.Projects {
		if p == req.Path {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "already registered"})
			return
		}
	}
	s.cfg.Projects = append(s.cfg.Projects, req.Path)
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveProject(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cfg.Projects[:0]
	for _, p := range s.cfg.Projects {
		if p != req.Path {
			out = append(out, p)
		}
	}
	s.cfg.Projects = out
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
	// Detach from the request context so closing the browser tab mid-sync does
	// not cancel in-flight git subprocesses and leave repos half-fetched.
	sum := s.SyncAll(context.WithoutCancel(r.Context()), req.Force)
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
