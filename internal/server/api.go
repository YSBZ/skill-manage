package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"skillmanage/internal/adopt"
	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
	"skillmanage/internal/harness"
	"skillmanage/internal/linker"
	"skillmanage/internal/reconcile"
	"skillmanage/internal/scanner"
	"skillmanage/internal/source"
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
	mux.HandleFunc("GET /api/inventory", s.requireAuth(s.handleInventory))
	mux.HandleFunc("DELETE /api/inventory/link", s.requireAuth(s.handleDeleteStrayLink))
	mux.HandleFunc("POST /api/ignore-plugins", s.requireAuth(s.handleSetIgnorePlugins))
	mux.HandleFunc("POST /api/adopt", s.requireAuth(s.handleAdopt))
	mux.HandleFunc("POST /api/enabled", s.requireAuth(s.handleAddEnabled))
	mux.HandleFunc("DELETE /api/enabled", s.requireAuth(s.handleRemoveEnabled))
	mux.HandleFunc("POST /api/targets", s.requireAuth(s.handleAddTarget))
	mux.HandleFunc("DELETE /api/targets", s.requireAuth(s.handleRemoveTarget))
	mux.HandleFunc("POST /api/targets/reorder", s.requireAuth(s.handleReorderTargets))
	mux.HandleFunc("GET /api/browse", s.requireAuth(s.handleBrowse))
	mux.HandleFunc("GET /api/credentials", s.requireAuth(s.handleListCredentials))
	mux.HandleFunc("POST /api/credentials", s.requireAuth(s.handleSetCredential))
	mux.HandleFunc("DELETE /api/credentials", s.requireAuth(s.handleDeleteCredential))
	mux.HandleFunc("POST /api/dirsource/update", s.requireAuth(s.handleDirSourceUpdate))
	mux.HandleFunc("POST /api/update-now", s.requireAuth(s.handleUpdateNow))
	mux.HandleFunc("POST /api/apply", s.requireAuth(s.handleApply))

	mux.HandleFunc("/", s.hostGuard(s.spaHandler()))
	return mux
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
	GitError    string                `json:"gitError,omitempty"`      // set when git is unavailable
	NpxAvailable bool                 `json:"npxAvailable,omitempty"` // true → UI offers one-click skills.sh update (U7)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := statusResp{
		FirstRun:    s.firstRun,
		Enabled:     s.cfg.Enabled,
		Targets:     s.cfg.Targets,
		Links:       s.manifest.Links,
		LastSummary:  s.lastSummary,
		GitError:     s.gitErr,
		NpxAvailable: s.npxPath != "",
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
	name := reconcile.RepoName(req.URL)
	s.mu.Lock()
	out := s.cfg.Repos[:0]
	for _, repo := range s.cfg.Repos {
		if repo.URL != req.URL {
			out = append(out, repo)
		}
	}
	s.cfg.Repos = out
	// Drop this repo's skill selections so reconcile no longer wants their links
	// and tears them off disk. Enabled.Skill is "<repoName>/<skill>".
	kept := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if !strings.HasPrefix(e.Skill, name+"/") {
			kept = append(kept, e)
		}
	}
	s.cfg.Enabled = kept
	delete(s.repoStatus, req.URL)
	// Drop the stored HTTPS credential for this repo's host — but only if no
	// remaining repo still uses that host (credentials are keyed per host, and
	// several repos can share one server).
	if host := httpsHostOf(req.URL); host != "" {
		stillUsed := false
		for _, repo := range s.cfg.Repos {
			if httpsHostOf(repo.URL) == host {
				stillUsed = true
				break
			}
		}
		if !stillUsed {
			if creds, err := config.LoadCredentials(s.centralDir); err == nil {
				if _, ok := creds.Hosts[host]; ok {
					delete(creds.Hosts, host)
					_ = config.SaveCredentials(s.centralDir, creds)
				}
			}
		}
	}
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	reposRoot := s.reposRoot
	s.mu.Unlock()
	// Remove the local mirror and reconcile now so the repo's symlinks are gone
	// immediately, not on the next apply.
	_ = os.RemoveAll(filepath.Join(reposRoot, name))
	s.ReconcileOnly()
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

type browseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type browseResp struct {
	Path   string        `json:"path"`   // resolved absolute directory
	Parent string        `json:"parent"` // parent dir, "" when at filesystem root
	Dirs   []browseEntry `json:"dirs"`   // immediate subdirectories
}

// handleBrowse lets the add-target modal pick a directory from the real
// filesystem instead of typing the path blind. Browsers can't read absolute
// paths from a native folder picker, so the daemon walks the tree on the
// server side. Listing is directory-only and gated behind token auth on a
// localhost-bound daemon — it exposes folder names, never file contents.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		if home, err := os.UserHomeDir(); err == nil {
			raw = home
		} else {
			raw = string(filepath.Separator)
		}
	}
	p := harness.Expand(raw)
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	p = filepath.Clean(p)
	info, err := os.Stat(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录不存在或无法访问"})
		return
	}
	if !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是目录"})
		return
	}
	entries, err := os.ReadDir(p) // already sorted by filename
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无法读取目录（权限不足？）"})
		return
	}
	dirs := []browseEntry{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirs = append(dirs, browseEntry{Name: e.Name(), Path: filepath.Join(p, e.Name())})
	}
	parent := filepath.Dir(p)
	if parent == p { // at filesystem root
		parent = ""
	}
	writeJSON(w, http.StatusOK, browseResp{Path: p, Parent: parent, Dirs: dirs})
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
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	s.mu.Lock()
	snapshot := config.Manifest{Links: append([]config.LinkRecord(nil), s.manifest.Links...)}
	roots := s.targetsLocked()
	includePlugins := s.cfg.IncludePluginSkills
	s.mu.Unlock()
	// Scope to one tab (sync dir) when ?target= is given, so the "未备份 skill"
	// list matches the active tab instead of merging every directory together.
	if target != "" {
		want := harness.Expand(target)
		scoped := roots[:0:0]
		for _, t := range roots {
			if harness.Expand(t.Dir) == want {
				scoped = append(scoped, t)
				break
			}
		}
		roots = scoped
	}
	list, err := adopt.ListAdoptable(roots, &snapshot, includePlugins, s.personalStore)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []adopt.Adoptable{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": list, "includePlugins": includePlugins})
}

// --- inventory (目录现状视图, phase 3 U6) ---

// inventoryItem is one skill physically present in a sync target, tagged with
// where it came from (KTD2/KTD4). It unifies what the old split UI showed across
// the repo catalog and the "未备份 skill" sidebar.
type inventoryItem struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Kind        source.SourceKind  `json:"kind"`
	Repo        string             `json:"repo,omitempty"`
	SourceURL   string             `json:"sourceUrl,omitempty"` // skills.sh only; http(s)-validated
	Scope       harness.SkillScope `json:"scope"`
	Selector    string             `json:"selector,omitempty"` // enabled selector for managed items (U9 toggle)
	Managed     bool               `json:"managed"`            // owned by SkillManage (git/local)
	Enabled     bool               `json:"enabled"`            // a managed link is materialized → enabled
	Follow      bool               `json:"follow,omitempty"`   // enabled via a whole-source follow (source/*) → no per-item disable
	LinkTarget  string             `json:"linkTarget,omitempty"` // for unknown links: where the symlink resolves (反查源头)
	Collision   bool               `json:"collision,omitempty"`
}

type inventoryResp struct {
	Target string             `json:"target"`
	Scope  harness.SkillScope `json:"scope"`
	Items  []inventoryItem    `json:"items"`
}

// handleInventory lists the skills actually present in one sync target, each
// attributed to a source (git / local / skills.sh / plugin / handwritten /
// unknown). It replaces the old "list repo candidates" main panel (R3.1). The
// manifest + config snapshot is taken under s.mu (consistent with reconcile, so
// no TOCTOU); filesystem classification then runs on the snapshot.
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		http.Error(w, "missing target", http.StatusBadRequest)
		return
	}
	want := harness.Expand(target)

	s.mu.Lock()
	configured := ""
	for _, d := range s.cfg.Targets {
		if harness.Expand(d) == want {
			configured = d
			break
		}
	}
	manifest := config.Manifest{Links: append([]config.LinkRecord(nil), s.manifest.Links...)}
	conflicts := append([]linker.Conflict(nil), s.lastSummary.Conflicts...)
	// Whole-source follow entries (e.g. "myrepo/*", "@local/*") mapped to THIS
	// target: items they cover cannot be disabled individually (follow is
	// all-or-nothing), so the UI shows "跟随中" instead of a per-item toggle.
	followSrc := map[string]bool{}
	for _, e := range s.cfg.Enabled {
		if strings.HasSuffix(e.Skill, "/*") && harness.Expand(e.Target) == want {
			followSrc[strings.TrimSuffix(e.Skill, "/*")] = true
		}
	}
	dirSourcePaths := effectiveDirectorySources(s.cfg.DirectorySources)
	reposRoot, personalStore := s.reposRoot, s.personalStore
	targets := append([]string(nil), s.cfg.Targets...)
	s.mu.Unlock()

	if configured == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown sync target"})
		return
	}

	// Build the classifier inputs (resolved once per request).
	agentsSkillsRoot, lock := resolveAgentsLock(dirSourcePaths)
	var pluginRoots, allowed []string
	allowed = append(allowed, harness.Expand(reposRoot), harness.Expand(personalStore))
	for _, p := range dirSourcePaths {
		allowed = append(allowed, harness.Expand(p))
	}
	for _, t := range targets {
		pr := harness.Expand(harness.PluginRootFor(t))
		pluginRoots = append(pluginRoots, pr)
		allowed = append(allowed, pr)
	}
	clf := source.Classifier{
		Mgr:              linker.NewManager(reposRoot, personalStore),
		Manifest:         &manifest,
		AgentsSkillsRoot: agentsSkillsRoot,
		Lock:             lock,
		PluginRoots:      pluginRoots,
		AllowedRoots:     allowed,
	}
	scope := harness.Scope(configured)

	skills, err := scanner.ScanInventory(want)
	if err != nil {
		// target dir not present/readable → empty inventory, not an error.
		writeJSON(w, http.StatusOK, inventoryResp{Target: configured, Scope: scope, Items: []inventoryItem{}})
		return
	}
	items := make([]inventoryItem, 0, len(skills))
	for _, sk := range skills {
		res := clf.Classify(sk, configured)
		it := inventoryItem{
			Name:        sk.LinkName,
			Description: sk.Description,
			Kind:        res.Kind,
			Repo:        res.Repo,
			Scope:       scope,
			Managed:     res.Kind == source.KindGit || res.Kind == source.KindLocal,
			Collision:   shadowCollides(conflicts, sk.LinkName, configured),
		}
		if res.Kind == source.KindSkillsSh {
			it.SourceURL = validHTTPURL(res.SourceURL) // strip non-http(s) (XSS guard)
		}
		if res.Kind == source.KindUnknown {
			if rt, ok := clf.Mgr.ResolveLink(sk.Dir); ok {
				it.LinkTarget = rt // 反查：where the stray symlink points
			}
		}
		if it.Managed {
			it.Enabled = true // present + managed ⟹ the link is live
			src := res.Repo
			if res.Kind == source.KindLocal {
				src = reconcile.LocalNamespace
			}
			if src != "" {
				it.Selector = src + "/" + sk.LinkName
				it.Follow = followSrc[src] // covered by a whole-source follow → no per-item disable
			}
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, inventoryResp{Target: configured, Scope: scope, Items: items})
}

type strayLinkReq struct {
	Target string `json:"target"`
	Name   string `json:"name"`
}

// handleDeleteStrayLink removes a single stray symlink (an "unknown" inventory
// entry) the user explicitly chose to clean up. This is the ONE user-initiated
// exception to never-break (invariant ④, which governs AUTOMATIC reconcile):
// the request is explicit and confirmed in the UI. It is still tightly bounded —
// it removes ONLY the symlink, never the target it points at; refuses anything
// that is not a symlink (so a real directory's data is never deleted); and
// refuses links recorded in our manifest (use disable for those).
func (s *Server) handleDeleteStrayLink(w http.ResponseWriter, r *http.Request) {
	if !originLoopbackOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	var req strayLinkReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid link name"})
		return
	}
	want := harness.Expand(strings.TrimSpace(req.Target))
	s.mu.Lock()
	configured := ""
	for _, d := range s.cfg.Targets {
		if harness.Expand(d) == want {
			configured = d
			break
		}
	}
	owned := linker.NewManager(s.reposRoot, s.personalStore).FindOwned(&s.manifest, configured, name) != nil
	s.mu.Unlock()
	if configured == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown sync target"})
		return
	}
	if owned {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "这是本工具管理的链接，请用「停用」"})
		return
	}
	p := filepath.Join(want, name)
	lst, err := os.Lstat(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "链接不存在"})
		return
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		// Never delete a real file/dir — only a symlink.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是软链，拒绝删除（只删软链，不动真实目录）"})
		return
	}
	if err := os.Remove(p); err != nil { // removes the symlink only, never its target
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// effectiveDirectorySources returns the configured directory-source paths plus
// any auto-discovered defaults (e.g. ~/.agents/skills), deduped by resolved path.
func effectiveDirectorySources(configured []config.DirectorySource) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		e := harness.Expand(p)
		if e == "" || seen[e] {
			return
		}
		seen[e] = true
		out = append(out, p)
	}
	for _, d := range configured {
		add(d.Path)
	}
	for _, d := range harness.DiscoverDefaultDirectorySources() {
		add(d)
	}
	return out
}

// resolveAgentsLock finds the ~/.agents/skills directory source (if any) and
// loads its sibling .skill-lock.json. skills.sh canonical lives at <agents>/skills
// with the lock at <agents>/.skill-lock.json.
func resolveAgentsLock(dirSourcePaths []string) (agentsSkillsRoot string, lock source.SkillLock) {
	lock = source.SkillLock{Skills: map[string]source.LockEntry{}}
	for _, p := range dirSourcePaths {
		e := harness.Expand(p)
		if filepath.Base(e) == "skills" && filepath.Base(filepath.Dir(e)) == ".agents" {
			agentsSkillsRoot = e
			if l, err := source.LoadSkillLock(filepath.Dir(e)); err == nil {
				lock = l
			}
			return
		}
	}
	return
}

// shadowCollides reports whether name has a cross-target shadow conflict (U3 /
// ConflictShadow) involving this target — i.e. the same name is mapped under
// more than one target of the same harness, so the project-level one wins and
// the user-level one is shadowed (R5.1, surfaced for display per AE3).
func shadowCollides(conflicts []linker.Conflict, name, target string) bool {
	want := harness.Expand(target)
	for _, c := range conflicts {
		if c.Kind != linker.ConflictShadow || c.LinkName != name {
			continue
		}
		for _, t := range c.Targets {
			if harness.Expand(t) == want {
				return true
			}
		}
	}
	return false
}

// validHTTPURL returns raw only when it is an http(s) URL, else "". Lockfile
// sourceUrl is third-party-written; this strips javascript:/data: payloads
// before the value can reach the UI (XSS guard, U6/U8).
func validHTTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return raw
	}
	return ""
}

// --- skills.sh update delegation (phase 3 U7) ---

// skillsRunner runs `npx skills update <name>` for a directory source. Split by
// platform (runner_unix.go / runner_windows.go) so Windows can route through
// cmd /c and suppress the console window the windowless daemon would flash.
type skillsRunner interface {
	UpdateSkill(ctx context.Context, npxPath, name string) (stdout, stderr string, err error)
}

// skillNameRe bounds the skill name passed to npx: a leading alnum/underscore
// then alnum/dot/dash/underscore. No path separators, no shell metacharacters,
// no leading dash (which npx would read as a flag). This is the first of two
// gates; the second is the on-disk allowlist (the name must be a real directory
// directly under ~/.agents/skills).
var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

type dirSourceUpdateReq struct {
	Name string `json:"name"`
}

// handleDirSourceUpdate delegates a skills.sh-managed skill's update to its own
// tool (`npx skills update <name>`). It never writes ~/.agents itself (invariant
// ④). Security (KTD5): defensive Origin check on this command-executing
// endpoint, strict name regex, AND an on-disk allowlist so a forged or crafted
// name cannot steer npx — the name must be a real directory currently under
// ~/.agents/skills.
func (s *Server) handleDirSourceUpdate(w http.ResponseWriter, r *http.Request) {
	if !originLoopbackOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	var req dirSourceUpdateReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "." || name == ".." || !skillNameRe.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skill name"})
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	dirSources := effectiveDirectorySources(s.cfg.DirectorySources)
	s.mu.Unlock()

	if npx == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "npx 不可用，请手动运行 npx skills update"})
		return
	}
	agentsSkillsRoot, _ := resolveAgentsLock(dirSources)
	if agentsSkillsRoot == "" || !isDirectChildDir(agentsSkillsRoot, name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是 skills.sh 管理的 skill"})
		return
	}

	ctx, cancel := context.WithTimeout(s.detachedCtx(), 60*time.Second)
	defer cancel()
	stdout, stderr, err := runner.UpdateSkill(ctx, npx, name)
	resp := map[string]any{"ok": err == nil, "stdout": stdout, "stderr": stderr}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// isDirectChildDir reports whether name is a real directory sitting directly
// under root. name is already regex-validated to contain no separators, so this
// cannot traverse out of root.
func isDirectChildDir(root, name string) bool {
	fi, err := os.Stat(filepath.Join(root, name))
	return err == nil && fi.IsDir()
}

// originLoopbackOK rejects a request whose Origin/Referer is a non-loopback web
// origin — a forged cross-site POST. A missing Origin (curl, same-origin fetch
// that didn't set it) is allowed; the Bearer-token gate and Host check already
// bound this to a localhost client.
func originLoopbackOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		o = r.Header.Get("Referer")
	}
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1", "":
		return true
	default:
		return false
	}
}

type pluginPrefReq struct {
	Ignore bool `json:"ignore"`
}

// handleSetIgnorePlugins toggles whether plugin skills are hidden from the
// adoptable list. The checkbox is phrased as "忽略 plugin", so ignore=true maps
// to IncludePluginSkills=false.
func (s *Server) handleSetIgnorePlugins(w http.ResponseWriter, r *http.Request) {
	var req pluginPrefReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.IncludePluginSkills = !req.Ignore
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ignore": req.Ignore})
}

type adoptReq struct {
	ID     string `json:"id"`
	Root   string `json:"root"`   // absolute source root from /api/adoptable (relocate path)
	Src    string `json:"src"`    // plugin skill source dir (import path)
	Target string `json:"target"` // sync dir to map an imported plugin skill into
	Plugin bool   `json:"plugin"` // true → import-copy from a plugin tree, don't relocate
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	var req adoptReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Plugin {
		s.handleAdoptPlugin(w, req)
		return
	}
	if req.ID == "" || req.Root == "" {
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

// handleAdoptPlugin imports a plugin skill (copy into the personal store, NOT
// relocate — the plugin cache is owned by the agent's plugin system) and maps
// the resulting @local/<id> into the chosen sync dir. Gated on the
// IncludePluginSkills opt-in; the source must live under a configured target's
// plugin tree (so this can't import arbitrary client paths).
func (s *Server) handleAdoptPlugin(w http.ResponseWriter, req adoptReq) {
	if req.ID == "" || req.Src == "" || req.Target == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	src := harness.Expand(req.Src)
	wantTarget := harness.Expand(req.Target)
	s.mu.Lock()
	if !s.cfg.IncludePluginSkills {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error_code": "invalid", "error": "plugin import is disabled"})
		return
	}
	canonical, srcAllowed := "", false
	for _, t := range s.targetsLocked() {
		if harness.Expand(t.Dir) == wantTarget {
			canonical = t.Dir // keep the user-facing form for the enabled entry
		}
		pr := harness.PluginRootFor(t.Dir)
		if src == pr || strings.HasPrefix(src, pr+string(os.PathSeparator)) {
			srcAllowed = true
		}
	}
	if canonical == "" {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error_code": "invalid", "error": "unknown target"})
		return
	}
	if !srcAllowed {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error_code": "invalid", "error": "source is not under a known plugin tree"})
		return
	}
	if err := adopt.Import(src, req.ID, s.personalStore); err != nil {
		s.mu.Unlock()
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeJSON(w, adoptStatus(code), map[string]string{"error_code": code, "error": err.Error()})
		return
	}
	s.cfg.Enabled = ensureEnabled(s.cfg.Enabled, config.EnabledEntry{
		Skill: reconcile.LocalNamespace + "/" + req.ID, Target: canonical, Mode: config.ModeSnapshot,
	})
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	// Materialize the symlink into the tab now (ReconcileOnly re-locks).
	s.ReconcileOnly()
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
	wantTarget := harness.Expand(e.Target)
	for _, x := range s.cfg.Enabled {
		if x.Skill == e.Skill && harness.Expand(x.Target) == wantTarget {
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
	wantTarget := harness.Expand(e.Target)
	out := s.cfg.Enabled[:0]
	for _, x := range s.cfg.Enabled {
		if x.Skill == e.Skill && harness.Expand(x.Target) == wantTarget {
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
	// Expand the selection into the concrete cc/codex skills dirs it implies, so
	// picking a project root (or a .claude home) adds the real skill dirs with
	// correct labels instead of an unlabeled "unknown" parent. Picking a project
	// root that holds both adds two targets. Falls back to the dir verbatim when
	// nothing cc/codex is found (preserving the ability to add an unknown dir).
	dirs := harness.SkillDirsFor(dir)
	if len(dirs) == 0 {
		dirs = []string{dir}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var added []string
	for _, d := range dirs {
		// Dedup by resolved path so "~/.claude/skills", "~/.claude/skills/" and
		// the absolute form are treated as the same directory.
		want := harness.Expand(d)
		dup := false
		for _, ex := range s.cfg.Targets {
			if harness.Expand(ex) == want {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		s.cfg.Targets = append(s.cfg.Targets, d)
		added = append(added, d)
	}
	if len(added) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already a sync directory"})
		return
	}
	// Apply the alias. A lone add takes it verbatim; when the selection fanned
	// out into several skill dirs, suffix each with its harness (e.g. "dev cc",
	// "dev codex") so the tabs stay distinguishable.
	if alias != "" {
		if s.cfg.TargetAliases == nil {
			s.cfg.TargetAliases = map[string]string{}
		}
		for _, d := range added {
			if len(added) == 1 {
				s.cfg.TargetAliases[d] = alias
			} else {
				s.cfg.TargetAliases[d] = alias + " " + string(harness.Classify(d))
			}
		}
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusCreated, map[string][]string{"added": added})
}

func (s *Server) handleRemoveTarget(w http.ResponseWriter, r *http.Request) {
	var req targetReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dir := strings.TrimSpace(req.Dir)
	want := harness.Expand(dir)
	s.mu.Lock()
	// Drop the directory and any enabled selection that pointed at it. Compare by
	// resolved path so a form mismatch (trailing slash / ~ vs absolute) can't
	// leave a selection behind. Removing the selections makes those links no
	// longer desired, so the reconcile below tears them off disk.
	out := s.cfg.Targets[:0]
	for _, d := range s.cfg.Targets {
		if harness.Expand(d) != want {
			out = append(out, d)
		}
	}
	s.cfg.Targets = out
	// Drop the alias by resolved path too — matching how Targets/Enabled are
	// filtered above — so a differing dir form (trailing slash / ~ / absolute)
	// can't orphan the alias entry.
	for k := range s.cfg.TargetAliases {
		if harness.Expand(k) == want {
			delete(s.cfg.TargetAliases, k)
		}
	}
	kept := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if harness.Expand(e.Target) != want {
			kept = append(kept, e)
		}
	}
	s.cfg.Enabled = kept
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Reconcile immediately so every symlink this tab owned is torn down right
	// now, instead of relying on a separate apply round-trip from the client.
	sum := s.ReconcileOnly()
	writeJSON(w, http.StatusOK, sum)
}

type reorderReq struct {
	Dirs []string `json:"dirs"`
}

// handleReorderTargets reorders the sync directories (tabs) to match the order
// the user dragged them into. The request lists dirs in the desired order; we
// reorder cfg.Targets by that (matched by resolved path), keeping any target the
// client didn't mention at the end in its original relative order. Purely
// cosmetic — it touches no links.
func (s *Server) handleReorderTargets(w http.ResponseWriter, r *http.Request) {
	var req reorderReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pos := map[string]int{}
	for i, d := range req.Dirs {
		pos[harness.Expand(d)] = i
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at := func(dir string) int {
		if p, ok := pos[harness.Expand(dir)]; ok {
			return p
		}
		return 1 << 30 // unmentioned → keep at the end
	}
	sort.SliceStable(s.cfg.Targets, func(a, b int) bool { return at(s.cfg.Targets[a]) < at(s.cfg.Targets[b]) })
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// httpsHostOf returns the host of an http(s) URL, or "" for ssh/scp remotes
// (credentials only apply to HTTPS).
func httpsHostOf(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(l, "https://") && !strings.HasPrefix(l, "http://") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// --- credentials (HTTPS PAT per host) ---

type credReq struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// handleListCredentials returns the hosts that have a stored credential plus the
// username — NEVER the token. The UI uses it to show which repos are configured.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	creds, err := config.LoadCredentials(s.centralDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := []map[string]string{}
	for host, c := range creds.Hosts {
		out = append(out, map[string]string{"host": host, "username": c.Username})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetCredential stores (or replaces) the HTTPS credential for a host. The
// token is required; username may be empty (some hosts accept any/token-as-pass).
func (s *Server) handleSetCredential(w http.ResponseWriter, r *http.Request) {
	var req credReq
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Host) == "" || strings.TrimSpace(req.Token) == "" {
		http.Error(w, "bad request (host and token required)", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	creds, err := config.LoadCredentials(s.centralDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	creds.Hosts[strings.TrimSpace(req.Host)] = config.Credential{Username: strings.TrimSpace(req.Username), Token: req.Token}
	if err := config.SaveCredentials(s.centralDir, creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteCredential removes a host's stored credential.
func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	var req credReq
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Host) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	creds, err := config.LoadCredentials(s.centralDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	delete(creds.Hosts, strings.TrimSpace(req.Host))
	if err := config.SaveCredentials(s.centralDir, creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
