package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"skillmanage/internal/adopt"
	"skillmanage/internal/browser"
	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
	"skillmanage/internal/harness"
	"skillmanage/internal/linker"
	"skillmanage/internal/pathutil"
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
	mux.HandleFunc("DELETE /api/inventory/handwritten", s.requireAuth(s.handleDeleteHandwritten))
	mux.HandleFunc("DELETE /api/local-skill", s.requireAuth(s.handleDeleteLocalSkill))
	mux.HandleFunc("POST /api/ignore-plugins", s.requireAuth(s.handleSetIgnorePlugins))
	mux.HandleFunc("POST /api/adopt", s.requireAuth(s.handleAdopt))
	mux.HandleFunc("POST /api/contribute", s.requireAuth(s.handleContribute))
	mux.HandleFunc("POST /api/quickupload", s.requireAuth(s.handleQuickUpload))
	mux.HandleFunc("GET /api/repo-drift", s.requireAuth(s.handleRepoDrift))
	mux.HandleFunc("GET /api/skill-authorship", s.requireAuth(s.handleSkillAuthorship))
	mux.HandleFunc("GET /api/repo-creators", s.requireAuth(s.handleRepoCreators))
	mux.HandleFunc("POST /api/upload", s.requireAuth(s.handleUpload))
	mux.HandleFunc("DELETE /api/repo-skill", s.requireAuth(s.handleDeleteRepoSkill))
	mux.HandleFunc("POST /api/move-local", s.requireAuth(s.handleMoveLocal))
	mux.HandleFunc("POST /api/move", s.requireAuth(s.handleMove))
	mux.HandleFunc("POST /api/local-source", s.requireAuth(s.handleAddLocalSource))
	mux.HandleFunc("DELETE /api/local-source", s.requireAuth(s.handleRemoveLocalSource))
	mux.HandleFunc("POST /api/enabled", s.requireAuth(s.handleAddEnabled))
	mux.HandleFunc("DELETE /api/enabled", s.requireAuth(s.handleRemoveEnabled))
	mux.HandleFunc("POST /api/targets", s.requireAuth(s.handleAddTarget))
	mux.HandleFunc("DELETE /api/targets", s.requireAuth(s.handleRemoveTarget))
	mux.HandleFunc("POST /api/targets/reorder", s.requireAuth(s.handleReorderTargets))
	mux.HandleFunc("POST /api/targets/alias", s.requireAuth(s.handleSetTargetAlias))
	mux.HandleFunc("GET /api/browse", s.requireAuth(s.handleBrowse))
	mux.HandleFunc("GET /api/credentials", s.requireAuth(s.handleListCredentials))
	mux.HandleFunc("POST /api/credentials", s.requireAuth(s.handleSetCredential))
	mux.HandleFunc("DELETE /api/credentials", s.requireAuth(s.handleDeleteCredential))
	mux.HandleFunc("POST /api/dirsource/update", s.requireAuth(s.handleDirSourceUpdate))
	mux.HandleFunc("POST /api/plugin/update", s.requireAuth(s.handlePluginUpdate))
	mux.HandleFunc("GET /api/plugins/installed", s.requireAuth(s.handlePluginsInstalled))
	mux.HandleFunc("GET /api/skillssh", s.requireAuth(s.handleListSkillsSh))
	mux.HandleFunc("GET /api/plugins", s.requireAuth(s.handleListPlugins))
	mux.HandleFunc("GET /api/plugin-skill", s.requireAuth(s.handlePluginSkillDetail))
	mux.HandleFunc("GET /api/skill-at", s.requireAuth(s.handleSkillAt))
	mux.HandleFunc("POST /api/skillssh/update-all", s.requireAuth(s.handleUpdateSkillsShAll))
	mux.HandleFunc("POST /api/skillssh/update", s.requireAuth(s.handleUpdateSkillsShOne))
	mux.HandleFunc("GET /api/skillssh/find", s.requireAuth(s.handleSkillsShFind))
	mux.HandleFunc("POST /api/skillssh/add", s.requireAuth(s.handleSkillsShAdd))
	mux.HandleFunc("POST /api/skillssh/remove", s.requireAuth(s.handleSkillsShRemove))
	mux.HandleFunc("POST /api/check-updates", s.requireAuth(s.handleCheckUpdates))
	mux.HandleFunc("GET /api/conflicts", s.requireAuth(s.handleConflicts))
	mux.HandleFunc("POST /api/update-now", s.requireAuth(s.handleUpdateNow))
	mux.HandleFunc("POST /api/repos/update", s.requireAuth(s.handleUpdateRepo))
	mux.HandleFunc("POST /api/apply", s.requireAuth(s.handleApply))
	mux.HandleFunc("POST /api/open", s.requireAuth(s.handleOpenExternal))

	mux.HandleFunc("/", s.hostGuard(s.spaHandler()))
	return mux
}

// dirHasContent reports whether dir exists and is non-empty — a cloned mirror.
func dirHasContent(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if code >= 400 { // log error responses to the daemon log for diagnosis
		if b, err := json.Marshal(v); err == nil {
			log.Printf("api error %d: %s", code, b)
		}
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes a {error_code, error} JSON body with the given status — the
// uniform error shape used across the API.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error_code": code, "error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// --- status ---

type statusResp struct {
	FirstRun     bool                  `json:"firstRun"`
	Repos        []RepoStatus          `json:"repos"`
	Enabled      []config.EnabledEntry `json:"enabled"`
	Targets      []string              `json:"targets"`
	Links        []config.LinkRecord   `json:"links"`
	LastSummary  reconcile.Summary     `json:"lastSummary"`
	GitError     string                `json:"gitError,omitempty"`     // set when git is unavailable
	NpxAvailable bool                  `json:"npxAvailable,omitempty"` // true → UI offers one-click skills.sh update (U7)
	ClaudeCLI    bool                  `json:"claudeCli,omitempty"`    // true → UI offers delegated plugin update (claude plugin update)
	SkillsSh     *skillsShInfo         `json:"skillsSh,omitempty"`     // skills.sh (vercel-labs/skills) directory source, if present
	PluginCount  int                   `json:"pluginCount,omitempty"`  // number of plugin-provided skills (read-only category)
	LocalSources []dirSourceInfo       `json:"localSources,omitempty"` // user-registered local folder sources (each its own card)
}

// dirSourceInfo describes one user-registered local directory source for the
// sidebar: its stable id (selector namespace "@dir:<id>"), display label, path,
// and current skill count (scanned live).
type dirSourceInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// skillsShInfo describes the skills.sh directory source for the sidebar entry:
// it is a read-only "库" managed by another tool (npx skills), never owned by us.
type skillsShInfo struct {
	Root  string `json:"root"`  // ~/.agents/skills
	Count int    `json:"count"` // number of skills.sh-managed skills there
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := statusResp{
		FirstRun:     s.firstRun,
		Enabled:      s.cfg.Enabled,
		Targets:      s.cfg.Targets,
		Links:        s.manifest.Links,
		LastSummary:  s.lastSummary,
		GitError:     s.gitErr,
		NpxAvailable: s.npxPath != "",
		ClaudeCLI:    s.claudePath != "",
	}
	// repo status in config order
	for _, repo := range s.cfg.Repos {
		if st, ok := s.repoStatus[repo.URL]; ok {
			resp.Repos = append(resp.Repos, st)
			continue
		}
		// No in-memory status (e.g. fresh daemon start — repoStatus is in-memory and
		// there is no launch auto-sync anymore). Look at disk: a populated mirror was
		// cloned before, so report "stale" (已克隆·本会话未拉取 → UI 显示「未更新」)
		// rather than the misleading "never-synced". Only a truly absent/empty mirror
		// is never-synced.
		name := reconcile.RepoName(repo.URL)
		st := RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: name, State: "never-synced"}
		if dirHasContent(filepath.Join(s.reposRoot, name)) {
			st.State = "stale"
		}
		resp.Repos = append(resp.Repos, st)
	}
	// skills.sh directory source (read-only, managed by `npx skills`).
	if skills := s.listSkillsShLocked(); len(skills) > 0 {
		root, _ := resolveAgentsLock(effectiveDirectorySources(s.cfg.DirectorySources))
		resp.SkillsSh = &skillsShInfo{Root: root, Count: len(skills)}
	}
	resp.PluginCount = len(listPluginSkills()) // plugin-provided skills (read-only)
	// User-registered local folder sources (each its own card; scanned live).
	for _, d := range s.cfg.LocalSources {
		count := 0
		if sk, err := scanner.Scan(harness.Expand(d.Path)); err == nil {
			count = len(sk)
		}
		resp.LocalSources = append(resp.LocalSources, dirSourceInfo{ID: d.ID, Label: d.Label, Path: d.Path, Count: count})
	}
	writeJSON(w, http.StatusOK, resp)
}

// listSkillsShLocked scans the ~/.agents/skills directory source and returns the
// skills skills.sh manages (present in its .skill-lock.json). Caller holds s.mu.
func (s *Server) listSkillsShLocked() []scanner.Skill {
	dirSources := effectiveDirectorySources(s.cfg.DirectorySources)
	root, lock := resolveAgentsLock(dirSources)
	if root == "" {
		return nil
	}
	all, err := scanner.Scan(root)
	if err != nil {
		return nil
	}
	var out []scanner.Skill
	for _, sk := range all {
		_, byLink := lock.Has(sk.LinkName)
		_, byDir := lock.Has(filepath.Base(sk.Dir))
		if byLink || byDir {
			out = append(out, sk)
		}
	}
	return out
}

// handleListSkillsSh lists the skills.sh-managed skills for the sidebar modal,
// each enriched with its lockfile source ("owner/repo") + sourceUrl so the UI can
// show where it came from and the per-skill `npx skills update <name>` command.
func (s *Server) handleListSkillsSh(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	dirSources := effectiveDirectorySources(s.cfg.DirectorySources)
	s.mu.Unlock()
	root, lock := resolveAgentsLock(dirSources)
	type skillsShItem struct {
		scanner.Skill
		Source    string `json:"source,omitempty"`    // "owner/repo" from the lockfile
		SourceURL string `json:"sourceUrl,omitempty"` // validated http(s) URL (XSS-safe)
	}
	out := []skillsShItem{}
	if root != "" {
		if all, err := scanner.Scan(root); err == nil {
			for _, sk := range all {
				e, ok := lock.Has(sk.LinkName)
				if !ok {
					e, ok = lock.Has(filepath.Base(sk.Dir))
				}
				if !ok {
					continue
				}
				out = append(out, skillsShItem{Skill: sk, Source: e.Source, SourceURL: validHTTPURL(e.SourceURL)})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateSkillsShAll runs `npx skills update` (no skill arg) to update every
// skills.sh-managed skill at once. Delegated update only — we never take ownership.
func (s *Server) handleUpdateSkillsShAll(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "npx 不可用，请手动运行 npx skills update"})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 180*time.Second)
	defer cancel()
	stdout, stderr, err := runner.UpdateAll(ctx, npx)
	resp := map[string]any{"ok": err == nil, "stdout": stdout, "stderr": stderr}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateSkillsShOne runs `npx skills update <name>` for a single
// skills.sh-managed skill (the per-card「更新」button). Delegated update only —
// we never take ownership. The name is allowlist-validated AND confirmed to be a
// direct child of the canonical ~/.agents/skills, so the arg can only name a real
// skills.sh skill (never a flag or traversal).
func (s *Server) handleUpdateSkillsShOne(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
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
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "npx 不可用，请手动运行 npx skills update " + name})
		return
	}
	agentsSkillsRoot, _ := resolveAgentsLock(dirSources)
	if agentsSkillsRoot == "" || !isDirectChildDir(agentsSkillsRoot, name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是 skills.sh 管理的 skill"})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 180*time.Second)
	defer cancel()
	stdout, stderr, err := runner.UpdateSkill(ctx, npx, name)
	resp := map[string]any{"ok": err == nil, "stdout": stdout, "stderr": stderr}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// skillsShResult is one parsed online search hit from `npx skills find`.
type skillsShResult struct {
	Pkg         string `json:"pkg"`   // owner/repo@skill — the exact arg for `skills add`
	Owner       string `json:"owner"` // owner segment, for display
	Repo        string `json:"repo"`  // repo segment, for display
	Skill       string `json:"skill"` // skill segment, for display
	Installs    int    `json:"installs"`
	InstallsRaw string `json:"installsRaw"` // original "484.6K" string, kept for display
	URL         string `json:"url"`
}

// skillsPkgRe is the allowlist for a skills.sh package id (owner/repo@skill). It
// is deliberately NOT pluginRefRe/skillNameRe — those forbid the slash this form
// requires. Each segment must start alnum (blocks a leading "-" flag and ".."
// traversal), and the @-form is mandatory; the skill segment may contain ":".
var skillsPkgRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// skillsFindLineRe matches a result line of `npx skills find` after ANSI strip:
// "owner/repo@skill   484.6K installs". pkg is the strict capture (load-bearing
// for add + dedup); the installs count is lenient (sort-only).
var skillsFindLineRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9._:-]*)\s+([0-9][0-9.]*[KMBkmb]?)\s+installs?\b`)

// skillsFindURLRe pulls the http(s) URL off the "└ https://skills.sh/…" line.
var skillsFindURLRe = regexp.MustCompile(`(https?://[^\s]+)`)

// parseInstalls turns "484.6K" / "1.2M" / "1K" / "42" into an int (sort key).
// Returns 0 on any parse failure — installs is display/sort only, never gates add.
func parseInstalls(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	mult := 1.0
	switch raw[len(raw)-1] {
	case 'K', 'k':
		mult, raw = 1e3, raw[:len(raw)-1]
	case 'M', 'm':
		mult, raw = 1e6, raw[:len(raw)-1]
	case 'B', 'b':
		mult, raw = 1e9, raw[:len(raw)-1]
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int(f * mult)
}

// splitPkg breaks owner/repo@skill into its three display parts. Lenient: missing
// segments come back empty rather than erroring (the caller already validated pkg).
func splitPkg(pkg string) (owner, repo, skill string) {
	left := pkg
	if at := strings.IndexByte(pkg, '@'); at >= 0 {
		left, skill = pkg[:at], pkg[at+1:]
	}
	if sl := strings.IndexByte(left, '/'); sl >= 0 {
		owner, repo = left[:sl], left[sl+1:]
	} else {
		repo = left
	}
	return
}

// parseSkillsFind parses `npx skills find` text output (the CLI has no --json for
// find, KTD2). Strips ANSI, extracts each "pkg N installs" line + its following
// "└ url" line. Unparseable lines are skipped (format-drift tolerance); a parsed
// pkg that would not round-trip through skillsPkgRe is dropped (can't be installed).
// Results are sorted by install count, descending.
func parseSkillsFind(stdout string) []skillsShResult {
	lines := strings.Split(ansiRe.ReplaceAllString(stdout, ""), "\n")
	out := []skillsShResult{}
	for i := 0; i < len(lines); i++ {
		m := skillsFindLineRe.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			continue
		}
		pkg := m[1]
		if !skillsPkgRe.MatchString(pkg) {
			continue
		}
		owner, repo, skill := splitPkg(pkg)
		res := skillsShResult{Pkg: pkg, Owner: owner, Repo: repo, Skill: skill, Installs: parseInstalls(m[2]), InstallsRaw: m[2]}
		// URL is on one of the next 1-2 lines ("└ https://…").
		for j := i + 1; j < len(lines) && j <= i+2; j++ {
			if um := skillsFindURLRe.FindStringSubmatch(lines[j]); um != nil {
				res.URL = um[1]
				break
			}
		}
		out = append(out, res)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Installs > out[b].Installs })
	return out
}

// handleSkillsShFind searches the skills.sh online registry via `npx skills find`
// (KTD2: text output, parsed). Guards: empty query → no npx call; a query that
// would be read as a flag (leading "-") → 400 (flag-injection guard). npx absent
// → {available:false} so the UI degrades the online section (R7) without touching
// local search.
func (s *Server) handleSkillsShFind(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "results": []skillsShResult{}})
		return
	}
	if strings.HasPrefix(q, "-") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "搜索词不能以 - 开头"})
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 120*time.Second)
	defer cancel()
	stdout, stderr, err := runner.SkillsFind(ctx, npx, q)
	if err != nil {
		// Honest: surface the failure rather than reporting an empty result set.
		writeJSON(w, http.StatusOK, map[string]any{
			"available": true, "results": []skillsShResult{},
			"error": firstFailureLine(ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "results": parseSkillsFind(stdout)})
}

// skillsAddReq is the body of POST /api/skillssh/add.
type skillsAddReq struct {
	Pkg string `json:"pkg"`
}

// handleSkillsShAdd installs a skills.sh package globally into the canonical
// ~/.agents/skills via `npx skills add <pkg> -g -y` (KTD1). Non-ownership: we only
// shell out; updates go back through `npx skills update`. Honest outcome (KTD5):
// the CLI is not exit-code-truthful, so a "✘ … Failed" with exit 0 is classified
// as failed, not a silent success. The new skill surfaces on the next /api/skillssh
// + inventory fetch (computed on demand), so no server-side cache to invalidate.
func (s *Server) handleSkillsShAdd(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req skillsAddReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pkg := strings.TrimSpace(req.Pkg)
	if !skillsPkgRe.MatchString(pkg) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的 skill 包名（应形如 owner/repo@skill）"})
		return
	}
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "npx 不可用，请手动运行 npx skills add"})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 300*time.Second)
	defer cancel()
	stdout, stderr, err := runner.SkillsAdd(ctx, npx, pkg)
	clean := ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")
	// Honest classification, calibrated against the real CLI (KTD5):
	//   success → "✓ <skill> (copied)" + "Installed" (re-install is idempotent, same
	//             markers; we report it as installed too — harmless).
	//   failure → no success marker; e.g. "■ No matching skills found for: X". The
	//             CLI does NOT print "Failed"/"✗" for a missing package, so we treat
	//             absence-of-success as failure rather than scanning for a fail word.
	status := "installed"
	if err != nil || !(strings.Contains(clean, "✓") || strings.Contains(clean, "Installed") || strings.Contains(clean, "copied")) {
		status = "failed"
	}
	ok := status != "failed"
	resp := map[string]any{"ok": ok, "status": status, "stdout": stdout, "stderr": stderr}
	if !ok {
		if err != nil {
			resp["error"] = err.Error()
		} else {
			resp["error"] = skillsAddFailReason(clean)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// skillsAddFailReason extracts the human-meaningful failure line from `skills add`
// output (strips the CLI's box-drawing/status glyph prefixes), e.g.
// "No matching skills found for: X" or "… does not support global skill installation".
func skillsAddFailReason(clean string) string {
	for _, ln := range strings.Split(clean, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "■✗✘●◇│└├╮╯ "))
		if ln == "" {
			continue
		}
		if strings.Contains(ln, "No matching") || strings.Contains(ln, "Failed") || strings.Contains(ln, "Error") || strings.Contains(ln, "not found") || strings.Contains(ln, "not support") {
			return ln
		}
	}
	return "安装失败（CLI 未报告原因）"
}

// skillsRemoveReq is the body of POST /api/skillssh/remove.
type skillsRemoveReq struct {
	Name string `json:"name"`
}

// handleSkillsShRemove uninstalls a skills.sh skill: it (1) tears down SkillManage's
// own manifest-owned links for `@agents/<name>` across all targets via reconcile,
// then (2) delegates `npx skills remove <name> -g -a '*' -y` to drop skills.sh's
// canonical + every agent symlink it made. So "uninstall" removes ALL links (ours
// and skills.sh's) plus the real file. Each tool removes only what it owns —
// invariant ④ holds (we never rm ~/.agents ourselves; skills.sh does).
func (s *Server) handleSkillsShRemove(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req skillsRemoveReq
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
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "npx 不可用，请手动运行 npx skills remove"})
		return
	}
	agentsSkillsRoot, _ := resolveAgentsLock(dirSources)
	if agentsSkillsRoot == "" || !isDirectChildDir(agentsSkillsRoot, name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是 skills.sh 管理的 skill"})
		return
	}
	// Drop our own manifest-owned links for @agents/<name> across all targets, then
	// reconcile to tear them off disk — before skills.sh removes the canonical.
	sel := reconcile.AgentsNamespace + "/" + name
	s.mu.Lock()
	kept := s.cfg.Enabled[:0]
	removedLinks := 0
	for _, e := range s.cfg.Enabled {
		if e.Skill == sel {
			removedLinks++
			continue
		}
		kept = append(kept, e)
	}
	s.cfg.Enabled = kept
	var saveErr error
	if removedLinks > 0 {
		saveErr = config.SaveConfig(s.centralDir, s.cfg)
	}
	s.mu.Unlock()
	if saveErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": saveErr.Error()})
		return
	}
	if removedLinks > 0 {
		s.ReconcileOnly()
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 120*time.Second)
	defer cancel()
	stdout, stderr, err := runner.SkillsRemove(ctx, npx, name)
	clean := ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")
	// Honest: success markers are ✓ / "Removed"; absence ⇒ failed (KTD5 posture).
	status := "removed"
	if err != nil || !(strings.Contains(clean, "✓") || strings.Contains(clean, "Removed") || strings.Contains(clean, "removed")) {
		status = "failed"
	}
	ok := status != "failed"
	resp := map[string]any{"ok": ok, "status": status, "stdout": stdout, "stderr": stderr, "removedLinks": removedLinks}
	if !ok {
		if err != nil {
			resp["error"] = err.Error()
		} else {
			resp["error"] = skillsAddFailReason(clean)
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

// handleDeleteLocalSkill removes a backed-up local skill from the personal store
// (~/.skillmanage/local/<name>) and tears down every link it owns. This deletes
// the canonical copy, so it is gated like handleRemoveRepo: explicit + confirmed.
// Git-repo skills never reach here (the UI offers delete only for @local).
func (s *Server) handleDeleteLocalSkill(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req strayLinkReq // reuse {name} (target unused here)
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skill name"})
		return
	}
	sel := reconcile.LocalNamespace + "/" + name
	s.mu.Lock()
	// Drop selections for this local skill so reconcile removes its links.
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
	personalStore := s.personalStore
	s.mu.Unlock()
	// Resolve the real canonical dir by LinkName (basename may differ from the
	// sanitized name, e.g. ":" → "-"); fall back to the literal join.
	dir := filepath.Join(personalStore, name)
	if inv, err := scanner.ScanInventory(personalStore); err == nil {
		for _, sk := range inv {
			if sk.LinkName == name {
				dir = sk.Dir
				break
			}
		}
	}
	_ = os.RemoveAll(dir) // remove the canonical copy
	s.ReconcileOnly()     // tear down its links now
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleOpenExternal opens an external URL in the OS default browser. The desktop
// WKWebView ignores <a target="_blank"> (clicks no-op), so the UI routes outbound
// links here; browser.Open works identically in the desktop window and the
// browser daemon (both on the same machine). Only http(s) with a host is allowed
// — never arbitrary strings handed to the OS opener.
func (s *Server) handleOpenExternal(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "仅支持 http/https 链接"})
		return
	}
	if err := browser.Open(u.String()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleExportRepos writes the git-repo list to a JSON file on disk and reveals
// it in the OS file manager, returning {path, count}. It does NOT stream the
// bytes for a browser download: the desktop app's WKWebView has no download
// support, so the old Blob + <a download> path silently no-ops there. Writing
// server-side works identically in the desktop window and the browser daemon
// (both run on the same machine).
func (s *Server) handleExportRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repos := s.cfg.Repos
	s.mu.Unlock()
	data, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Optional ?dir= lets the user pick the destination folder (via the in-app
	// directory browser); empty falls back to Downloads / the central dir.
	var path string
	if dir := strings.TrimSpace(r.URL.Query().Get("dir")); dir != "" {
		p := harness.Expand(dir)
		if !filepath.IsAbs(p) {
			if abs, e := filepath.Abs(p); e == nil {
				p = abs
			}
		}
		p = filepath.Clean(p)
		if info, e := os.Stat(p); e != nil || !info.IsDir() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "导出目录不存在或不是文件夹"})
			return
		}
		path = filepath.Join(p, "skillmanage-repos.json")
	} else {
		path = exportFilePath(s.centralDir)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = browser.Reveal(path) // best-effort; the path is also returned as text
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "count": len(repos)})
}

// exportFilePath picks where the repo-list export lands: the user's Downloads
// folder when it exists (most discoverable for "move to another machine"),
// otherwise the central dir (always writable).
func exportFilePath(centralDir string) string {
	const name = "skillmanage-repos.json"
	if home, err := os.UserHomeDir(); err == nil {
		dl := filepath.Join(home, "Downloads")
		if fi, err := os.Stat(dl); err == nil && fi.IsDir() {
			return filepath.Join(dl, name)
		}
	}
	return filepath.Join(centralDir, name)
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

// handleSkillAt returns the SKILL.md of a skill by its physical location in a
// sync target (matched by LinkName via an inventory scan), so the detail view
// works for EVERY inventory kind — skills.sh, plugin, handwritten, unknown —
// not just the managed git/local skills that resolve to a known source root.
// Read-only: it only reads SKILL.md, never writes.
func (s *Server) handleSkillAt(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	want := harness.Expand(strings.TrimSpace(r.URL.Query().Get("target")))
	if name == "" || want == "" {
		http.Error(w, "missing target or name", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ok := false
	for _, d := range s.cfg.Targets {
		if harness.Expand(d) == want {
			ok = true
			break
		}
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown sync target", http.StatusBadRequest)
		return
	}
	inv, err := scanner.ScanInventory(want)
	if err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	for _, sk := range inv {
		if sk.LinkName == name || sk.LogicalName == name {
			content, _ := os.ReadFile(filepath.Join(sk.Dir, "SKILL.md"))
			src, url := s.skillsShSource(sk.Dir, sk.LinkName)
			writeJSON(w, http.StatusOK, skillDetail{
				LinkName:    sk.LinkName,
				LogicalName: sk.LogicalName,
				Description: sk.Description,
				Content:     string(content),
				Source:      src,
				SourceURL:   url,
			})
			return
		}
	}
	http.Error(w, "skill not found", http.StatusNotFound)
}

// pluginSkill is one skill provided by an installed plugin (read-only — plugins
// are managed by the harness's own plugin system, never by SkillManage).
type pluginSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Plugin      string `json:"plugin"`  // owning plugin (key before "@")
	Version     string `json:"version"` // installed version, if known
	Harness     string `json:"harness"` // cc | codex — which harness home this plugin lives under
}

// listPluginSkills reads each harness's plugins/installed_plugins.json and scans
// every installed plugin's skills/ dir, tagging each skill with its owning
// plugin. The manifest is the authoritative map (active install per plugin), so
// stale cache versions are not double-counted.
func listPluginSkills() []pluginSkill {
	var out []pluginSkill
	seen := map[string]bool{} // dedupe by plugin+skill across harness homes
	homes := []string{}
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(h, ".claude"), filepath.Join(h, ".codex"))
	}
	if ch := os.Getenv("CODEX_HOME"); ch != "" {
		homes = append(homes, ch)
	}
	for _, home := range homes {
		// harness tag from the home dir: .claude→cc, .codex→codex (so the UI can
		// filter plugin skills by the current tab's harness).
		harness := "cc"
		switch filepath.Base(home) {
		case ".codex":
			harness = "codex"
		case ".claude":
			harness = "cc"
		}
		manifest := filepath.Join(home, "plugins", "installed_plugins.json")
		data, err := os.ReadFile(manifest)
		if err != nil {
			continue
		}
		var raw struct {
			Plugins map[string][]struct {
				InstallPath string `json:"installPath"`
				Version     string `json:"version"`
			} `json:"plugins"`
		}
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		for key, installs := range raw.Plugins {
			plugin := key
			if at := strings.IndexByte(key, '@'); at >= 0 {
				plugin = key[:at]
			}
			for _, inst := range installs {
				if inst.InstallPath == "" {
					continue
				}
				skills, err := scanner.Scan(filepath.Join(inst.InstallPath, "skills"))
				if err != nil {
					continue
				}
				for _, sk := range skills {
					k := harness + "\x00" + plugin + "\x00" + sk.LinkName // dedupe per harness
					if seen[k] {
						continue
					}
					seen[k] = true
					out = append(out, pluginSkill{Name: sk.LinkName, Description: sk.Description, Plugin: plugin, Version: inst.Version, Harness: harness})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	skills := listPluginSkills()
	if skills == nil {
		skills = []pluginSkill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

// handlePluginSkillDetail reads one plugin skill's SKILL.md from its install
// tree (read-only). Matches by plugin + harness + skill name so we never read an
// arbitrary path — only a skill that actually appears in the plugin manifest.
func (s *Server) handlePluginSkillDetail(w http.ResponseWriter, r *http.Request) {
	plugin := r.URL.Query().Get("plugin")
	name := r.URL.Query().Get("name")
	harness := r.URL.Query().Get("harness")
	if plugin == "" || name == "" {
		http.Error(w, "missing plugin or name", http.StatusBadRequest)
		return
	}
	homes := []string{}
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(h, ".claude"), filepath.Join(h, ".codex"))
	}
	if ch := os.Getenv("CODEX_HOME"); ch != "" {
		homes = append(homes, ch)
	}
	for _, home := range homes {
		hn := "cc"
		if filepath.Base(home) == ".codex" {
			hn = "codex"
		}
		if harness != "" && hn != harness {
			continue
		}
		data, err := os.ReadFile(filepath.Join(home, "plugins", "installed_plugins.json"))
		if err != nil {
			continue
		}
		var raw struct {
			Plugins map[string][]struct {
				InstallPath string `json:"installPath"`
			} `json:"plugins"`
		}
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		for key, installs := range raw.Plugins {
			p := key
			if at := strings.IndexByte(key, '@'); at >= 0 {
				p = key[:at]
			}
			if p != plugin {
				continue
			}
			for _, inst := range installs {
				if inst.InstallPath == "" {
					continue
				}
				skills, err := scanner.Scan(filepath.Join(inst.InstallPath, "skills"))
				if err != nil {
					continue
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
			}
		}
	}
	http.Error(w, "skill not found", http.StatusNotFound)
}

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
	if repo == reconcile.AgentsNamespace {
		s.mu.Lock()
		root := s.agentsRootLocked()
		s.mu.Unlock()
		if root == "" {
			return "", false
		}
		return harness.Expand(root), true
	}
	if id, ok := strings.CutPrefix(repo, reconcile.DirNamespacePrefix); ok {
		s.mu.Lock()
		path := s.dirSourceMapLocked()[id]
		s.mu.Unlock()
		if path == "" {
			return "", false
		}
		return path, true
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
	// Source / SourceURL are set only for skills.sh-managed skills: the lockfile
	// "owner/repo" and validated source URL, shown as 台账来源 in the detail view.
	Source    string `json:"source,omitempty"`
	SourceURL string `json:"sourceUrl,omitempty"`
}

// skillsShSource returns the skills.sh lockfile source ("owner/repo") and a
// validated source URL for a skill, but ONLY when its real path resolves under
// the skills.sh-managed agents root — so a git/local skill that happens to share
// a name with a lockfile entry never picks up a foreign source. Empty strings
// for non-skills.sh skills.
func (s *Server) skillsShSource(skillDir, name string) (src, url string) {
	s.mu.Lock()
	dirSources := effectiveDirectorySources(s.cfg.DirectorySources)
	s.mu.Unlock()
	root, lock := resolveAgentsLock(dirSources)
	if root == "" {
		return "", ""
	}
	real, err := filepath.EvalSymlinks(skillDir)
	if err != nil || !strings.HasPrefix(real, harness.Expand(root)) {
		return "", ""
	}
	if e, ok := lock.Has(name); ok {
		return e.Source, validHTTPURL(e.SourceURL)
	}
	return "", ""
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
			src, url := s.skillsShSource(sk.Dir, sk.LinkName)
			writeJSON(w, http.StatusOK, skillDetail{
				LinkName:    sk.LinkName,
				LogicalName: sk.LogicalName,
				Description: sk.Description,
				Content:     string(content),
				Source:      src,
				SourceURL:   url,
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
	Version     string             `json:"version,omitempty"` // SKILL.md frontmatter version, shown as a tag
	Kind        source.SourceKind  `json:"kind"`
	Repo        string             `json:"repo,omitempty"`
	SourceURL   string             `json:"sourceUrl,omitempty"` // skills.sh only; http(s)-validated
	Scope       harness.SkillScope `json:"scope"`
	Selector    string             `json:"selector,omitempty"`   // enabled selector for managed items (U9 toggle)
	Managed     bool               `json:"managed"`              // owned by SkillManage (git/local)
	Enabled     bool               `json:"enabled"`              // a managed link is materialized → enabled
	Follow      bool               `json:"follow,omitempty"`     // enabled via a whole-source follow (source/*) → no per-item disable
	LinkTarget  string             `json:"linkTarget,omitempty"` // for unknown links: where the symlink resolves (反查源头)
	Collision   bool               `json:"collision,omitempty"`
	// Dirty/DirtyKind flag a git-source skill with unpushed local changes
	// (新增/修改/已提交未推送), computed live so 快捷上传 appears the moment the
	// user edits — without waiting for the next sync.
	Dirty     bool              `json:"dirty,omitempty"`
	DirtyKind gitsync.DriftKind `json:"dirtyKind,omitempty"`
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
	localSrcMap := s.dirSourceMapLocked()
	reposRoot, personalStore := s.reposRoot, s.personalStore
	targets := append([]string(nil), s.cfg.Targets...)
	// Per-repo branch map + syncer handle for live git-drift detection below.
	repoBranch := map[string]string{}
	for _, rc := range s.cfg.Repos {
		repoBranch[reconcile.RepoName(rc.URL)] = rc.Branch
	}
	syncer := s.syncer
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
		DirSources:       localSrcMap,
	}
	// Local directory sources' own paths are valid link targets too (anti-escape).
	for _, p := range localSrcMap {
		clf.AllowedRoots = append(clf.AllowedRoots, p)
	}
	scope := harness.Scope(configured)

	skills, err := scanner.ScanInventory(want)
	if err != nil {
		// target dir not present/readable → empty inventory, not an error.
		writeJSON(w, http.StatusOK, inventoryResp{Target: configured, Scope: scope, Items: []inventoryItem{}})
		return
	}
	// driftByRepo memoizes one live Drift() per git repo present in this target,
	// so N skills from the same repo cost a single (local, no-network) git probe.
	driftByRepo := map[string]gitsync.Drift{}
	driftFor := func(repo string) gitsync.Drift {
		if syncer == nil || repo == "" {
			return gitsync.Drift{}
		}
		if d, ok := driftByRepo[repo]; ok {
			return d
		}
		d := syncer.Drift(r.Context(), filepath.Join(reposRoot, repo), repoBranch[repo])
		driftByRepo[repo] = d
		return d
	}

	items := make([]inventoryItem, 0, len(skills))
	for _, sk := range skills {
		res := clf.Classify(sk, configured)
		// A skills.sh skill WE linked via the @agents namespace is owned by us
		// (manifest) — so it's managed (can be 停用'd), even though its source is
		// skills.sh. A native skills.sh link we don't own stays read-only.
		ownedAgents := res.Kind == source.KindSkillsSh && clf.Mgr.FindOwned(clf.Manifest, configured, sk.LinkName) != nil
		it := inventoryItem{
			Name:        sk.LinkName,
			Description: sk.Description,
			Version:     sk.Version,
			Kind:        res.Kind,
			Repo:        res.Repo,
			Scope:       scope,
			Managed:     res.Kind == source.KindGit || res.Kind == source.KindLocal || res.Kind == source.KindDir || ownedAgents,
			Collision:   shadowCollides(conflicts, sk.LinkName, configured),
		}
		if res.Kind == source.KindSkillsSh {
			it.SourceURL = validHTTPURL(res.SourceURL) // strip non-http(s) (XSS guard)
		}
		if res.Kind == source.KindGit {
			if k, has := driftFor(res.Repo).Has(sk.LinkName); has {
				it.Dirty, it.DirtyKind = true, k
			}
		}
		if res.Kind == source.KindUnknown {
			// 反查源头：先看一跳指向，再尽量沿链完全解析到磁盘真实路径
			// （目标自身也可能是软链）。完全解析失败（断链等）就退回一跳。
			if rt, ok := clf.Mgr.ResolveLink(sk.Dir); ok {
				it.LinkTarget = rt
				if real, err := filepath.EvalSymlinks(sk.Dir); err == nil && real != "" {
					it.LinkTarget = real
				}
			}
		}
		if it.Managed {
			it.Enabled = true // present + managed ⟹ the link is live
			src := res.Repo
			if res.Kind == source.KindLocal {
				src = reconcile.LocalNamespace
			}
			if res.Kind == source.KindDir {
				src = reconcile.DirSelector(res.Repo) // "@dir:<id>" — local directory source
			}
			if ownedAgents {
				src = reconcile.AgentsNamespace // our @agents-linked skills.sh skill
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

// --- conflicts (撞名 / 遮蔽 / 嵌套 resolution) ---

type conflictCandidate struct {
	Selector    string `json:"selector"`    // verbatim enabled selector (for DELETE /api/enabled)
	Target      string `json:"target"`      // verbatim enabled target (config form)
	TargetLabel string `json:"targetLabel"` // alias or path, for display
	SourceLabel string `json:"sourceLabel"` // ns label (git repo / local / skills.sh / dir id)
	Follow      bool   `json:"follow"`      // selector is a whole-source follow (ns/*) — disabling drops the whole source
	Scope       string `json:"scope"`       // "user" (父/全局) or "project" (子/项目级) — drives 父/子 grouping
}

type conflictItem struct {
	Kind       string              `json:"kind"`
	LinkName   string              `json:"linkName"`
	Candidates []conflictCandidate `json:"candidates"`
}

// handleConflicts maps each detected conflict to the concrete enabled entries
// that produced it, so the UI can offer "keep one, disable the rest". Candidates
// are matched from cfg.Enabled (exact name or whole-source follow) within the
// conflict's target(s); selector/target are returned verbatim so DELETE
// /api/enabled removes exactly that entry.
func (s *Server) handleConflicts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	conflicts := append([]linker.Conflict(nil), s.lastSummary.Conflicts...)
	enabled := append([]config.EnabledEntry(nil), s.cfg.Enabled...)
	aliases := map[string]string{}
	for k, v := range s.cfg.TargetAliases {
		aliases[k] = v
	}
	dirLabels := map[string]string{}
	for _, d := range s.cfg.DirectorySources {
		dirLabels[reconcile.DirSelector(d.ID)] = d.Label
	}
	s.mu.Unlock()

	out := []conflictItem{}
	for _, c := range conflicts {
		item := conflictItem{Kind: string(c.Kind), LinkName: c.LinkName, Candidates: []conflictCandidate{}}
		tset := map[string]bool{}
		for _, t := range c.Targets {
			tset[harness.Expand(t)] = true
		}
		for _, e := range enabled {
			if !tset[harness.Expand(e.Target)] {
				continue
			}
			ns, name, ok := splitSelector(e.Skill)
			if !ok {
				continue
			}
			follow := name == "*"
			produces := false
			if follow {
				produces = s.sourceHasLink(ns, c.LinkName)
			} else {
				produces = pathutil.SanitizePathName(name) == c.LinkName
			}
			if !produces {
				continue
			}
			label := e.Target
			if a := aliases[e.Target]; a != "" {
				label = a
			}
			item.Candidates = append(item.Candidates, conflictCandidate{
				Selector: e.Skill, Target: e.Target, TargetLabel: label,
				SourceLabel: conflictSourceLabel(ns, dirLabels), Follow: follow,
				Scope: string(harness.Scope(e.Target)),
			})
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// splitSelector splits "ns/name" on the last slash; name "*" means whole-source.
func splitSelector(sel string) (ns, name string, ok bool) {
	i := strings.LastIndexByte(sel, '/')
	if i <= 0 || i == len(sel)-1 {
		return "", "", false
	}
	return sel[:i], sel[i+1:], true
}

func (s *Server) sourceHasLink(ns, linkName string) bool {
	root, ok := s.sourceRoot(ns)
	if !ok {
		return false
	}
	skills, err := scanner.Scan(root)
	if err != nil {
		return false
	}
	for _, sk := range skills {
		if sk.LinkName == linkName {
			return true
		}
	}
	return false
}

func conflictSourceLabel(ns string, dirLabels map[string]string) string {
	switch {
	case ns == reconcile.LocalNamespace:
		return "local"
	case ns == reconcile.AgentsNamespace:
		return "skills.sh"
	default:
		if l, ok := dirLabels[ns]; ok {
			return l
		}
		return ns // git repo name
	}
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
	if !originGuard(w, r) {
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
	// Resolve the real on-disk path by matching LinkName (the link's filename can
	// differ from the sanitized LinkName, e.g. a ":" → "-"), never reconstruct it
	// from name.
	inv, err := scanner.ScanInventory(want)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "链接不存在"})
		return
	}
	p := ""
	for _, sk := range inv {
		if sk.LinkName == name {
			p = sk.Dir
			break
		}
	}
	if p == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "链接不存在"})
		return
	}
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

// handleDeleteHandwritten deletes a handwritten skill — a REAL directory living
// directly in a sync target, not backed up to managed storage. Unlike the stray
// link delete (which only unlinks), this removes actual files, so it is gated
// hard: explicit + UI-confirmed, loopback/CSRF guarded, name sanitized, target
// must be a configured sync dir, the path must be a real directory (never a
// symlink — those go through 停用/删除软链), it must NOT be manifest-owned, and it
// must actually contain a SKILL.md (so we never RemoveAll an arbitrary folder).
func (s *Server) handleDeleteHandwritten(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req strayLinkReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skill name"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "这是本工具管理的 skill，请用「停用」"})
		return
	}
	// Resolve the REAL on-disk directory by matching LinkName — never reconstruct
	// the path from name. The directory basename can differ from the sanitized
	// LinkName (e.g. a ":" in the folder name becomes "-"), so a naive
	// filepath.Join(want, name) would miss the real dir ("目录不存在").
	inv, err := scanner.ScanInventory(want)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录不存在"})
		return
	}
	p := ""
	for _, sk := range inv {
		if sk.LinkName == name {
			p = sk.Dir
			break
		}
	}
	if p == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录不存在"})
		return
	}
	lst, err := os.Lstat(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录不存在"})
		return
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		// A symlink is not a handwritten real skill — use 停用/删除软链 instead.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "这是软链，不是手写真身，请用「停用 / 删除软链」"})
		return
	}
	if !lst.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不是目录，拒绝删除"})
		return
	}
	if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
		// Refuse to RemoveAll a folder that isn't actually a skill.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录内没有 SKILL.md，拒绝删除"})
		return
	}
	if err := os.RemoveAll(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// agentsRootLocked resolves the skills.sh shared dir (~/.agents/skills) for the
// reconciler's `@agents` namespace. Caller holds s.mu.
func (s *Server) agentsRootLocked() string {
	root, _ := resolveAgentsLock(effectiveDirectorySources(s.cfg.DirectorySources))
	return root
}

// dirSourceMapLocked builds the local directory-source registry (id → expanded
// path) the reconciler resolves "@dir:<id>" selectors against. Caller holds s.mu.
func (s *Server) dirSourceMapLocked() map[string]string {
	m := make(map[string]string, len(s.cfg.LocalSources))
	for _, d := range s.cfg.LocalSources {
		if d.ID != "" {
			m[d.ID] = harness.Expand(d.Path)
		}
	}
	return m
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
	UpdateAll(ctx context.Context, npxPath string) (stdout, stderr string, err error)
	// UpdatePlugin delegates a harness plugin update to its own CLI
	// (`<cliPath> plugin update <plugin> -s <scope>`); we never take ownership.
	UpdatePlugin(ctx context.Context, cliPath, plugin, scope string) (stdout, stderr string, err error)
	// ListPlugins runs `<cliPath> plugin list --json` to resolve each installed
	// plugin's id + scope for delegated update.
	ListPlugins(ctx context.Context, cliPath string) (stdout, stderr string, err error)
	// ListMarketplaces runs `<cliPath> plugin marketplace list --json` to tell
	// remote marketplaces from local directory ones.
	ListMarketplaces(ctx context.Context, cliPath string) (stdout, stderr string, err error)
	// SkillsFind runs `npx skills find <query>` to search the skills.sh online
	// registry. Output is text (the CLI has no --json for find); the caller parses.
	SkillsFind(ctx context.Context, npxPath, query string) (stdout, stderr string, err error)
	// SkillsAdd runs `npx skills add <pkg> -g -y` to install a skill globally into
	// skills.sh's canonical (~/.agents/skills), non-interactively. We never take
	// ownership — updates go back through `npx skills update`.
	SkillsAdd(ctx context.Context, npxPath, pkg string) (stdout, stderr string, err error)
	// SkillsRemove runs `npx skills remove <name> -g -a '*' -y` to uninstall a
	// skills.sh skill — its canonical and every agent symlink skills.sh made.
	SkillsRemove(ctx context.Context, npxPath, name string) (stdout, stderr string, err error)
}

// skillNameRe bounds the skill name passed to npx: a leading alnum/underscore
// then alnum/dot/dash/underscore. No path separators, no shell metacharacters,
// no leading dash (which npx would read as a flag). This is the first of two
// gates; the second is the on-disk allowlist (the name must be a real directory
// directly under ~/.agents/skills).
var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// pluginRefRe additionally allows the "name@marketplace" form used by
// `claude plugin update` (a bare name OR name@marketplace). No shell metachars.
var pluginRefRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(@[A-Za-z0-9._-]+)?$`)

// ansiRe strips terminal color escapes; the claude CLI may wrap JSON output in a
// colored proxy banner, which would otherwise break json.Unmarshal.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// validPluginScope is the allowlist for `claude plugin update -s <scope>`.
var validPluginScope = map[string]bool{"user": true, "project": true, "local": true, "managed": true}

// extractJSON pulls the JSON object/array out of CLI output that may carry a
// leading banner and trailing ANSI reset codes (strip ANSI, slice first {/[ to
// last }/]). Returns "" when no JSON span is found.
func extractJSON(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	i := strings.IndexAny(s, "{[")
	j := strings.LastIndexAny(s, "}]")
	if i < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

// installedPlugin is one entry of `claude plugin list` we care about.
type installedPlugin struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Scope   string `json:"scope"`
}

// pluginInstalledScope returns the scope `claude plugin list` records for the
// given plugin id (e.g. "project"), or "" if the listing fails or the id isn't
// found. Used to run the delegated update at the plugin's REAL scope instead of
// a client-supplied guess that would silently no-op.
func pluginInstalledScope(ctx context.Context, cli string, runner skillsRunner, id string) string {
	out, _, err := runner.ListPlugins(ctx, cli)
	if err != nil {
		return ""
	}
	var list []installedPlugin
	if json.Unmarshal([]byte(extractJSON(out)), &list) != nil {
		return ""
	}
	for _, p := range list {
		if p.ID == id {
			return p.Scope
		}
	}
	return ""
}

// firstFailureLine pulls the first human-meaningful failure line out of CLI
// output (the line carrying ✘ / "Failed" / "not installed"), so the UI can show
// why a delegated update that exited 0 actually failed.
func firstFailureLine(clean string) string {
	for _, ln := range strings.Split(clean, "\n") {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "✘"))
		if ln == "" {
			continue
		}
		if strings.Contains(ln, "Failed") || strings.Contains(ln, "not installed") {
			return strings.TrimSpace(ln)
		}
	}
	return "更新失败（CLI 未报告原因）"
}

// pluginNameOf returns the name part of a "name@marketplace" id.
func pluginNameOf(id string) string {
	if at := strings.IndexByte(id, '@'); at > 0 {
		return id[:at]
	}
	return id
}

// pluginMarketplaceOf returns the marketplace part of a "name@marketplace" id, or
// "" when absent. A marketplace of "local" marks a locally-developed plugin with
// no upstream — those are not delegate-updatable.
func pluginMarketplaceOf(id string) string {
	if at := strings.IndexByte(id, '@'); at > 0 && at+1 < len(id) {
		return id[at+1:]
	}
	return ""
}

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
	if !originGuard(w, r) {
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

type pluginUpdateReq struct {
	ID      string `json:"id"`    // full "name@marketplace" id
	Scope   string `json:"scope"` // install scope (user/project/local/managed)
	Harness string `json:"harness"`
}

// handlePluginUpdate delegates a plugin update to the harness's own CLI
// (`claude plugin update <id> -s <scope>`). We never take ownership of plugins
// (invariant ④) — this only invokes their native update. The UI only offers this
// for plugins the outdated check confirmed are updatable, so it passes the exact
// id + scope (a bare name or wrong scope makes the CLI report "not found").
func (s *Server) handlePluginUpdate(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req pluginUpdateReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(req.ID)
	if !pluginRefRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid plugin id"})
		return
	}
	if req.Harness != "" && req.Harness != "cc" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "暂仅支持 Claude Code 插件的委托更新"})
		return
	}
	scope := strings.TrimSpace(req.Scope)
	s.mu.Lock()
	cli, runner := s.claudePath, s.runner
	s.mu.Unlock()
	if cli == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "未找到 claude CLI，无法委托更新（确保 claude 在 PATH 中）"})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 120*time.Second)
	defer cancel()
	// Resolve the real install scope from the CLI rather than trusting the client
	// or defaulting blindly. `claude plugin update -s <wrong-scope>` EXITS 0 but
	// does nothing ("Plugin … is not installed at scope X") — which we'd otherwise
	// misreport as a successful update. The authoritative scope is whatever
	// `claude plugin list` records for this id.
	if real := pluginInstalledScope(ctx, cli, runner, id); real != "" {
		scope = real
	}
	if !validPluginScope[scope] {
		scope = "user"
	}
	stdout, stderr, err := runner.UpdatePlugin(ctx, cli, id, scope)
	// Classify the outcome HONESTLY. The CLI is not exit-code-truthful: a failed
	// update ("✘ Failed to update …") still exits 0, so err==nil alone does not
	// mean success. Inspect the (ANSI-stripped) output:
	//   - "already at the latest"  → current (no-op; why the version tag stays put)
	//   - "✘" / "Failed to update" / "not installed" → failed (surface the reason)
	//   - otherwise                → updated (restart Claude Code to apply)
	clean := ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")
	status := "updated"
	switch {
	case err != nil:
		status = "failed"
	case strings.Contains(clean, "already at the latest"):
		status = "current"
	case strings.Contains(clean, "✘") || strings.Contains(clean, "Failed to update") || strings.Contains(clean, "not installed"):
		status = "failed"
	}
	ok := status != "failed"
	resp := map[string]any{"ok": ok, "status": status, "stdout": stdout, "stderr": stderr}
	if !ok {
		if err != nil {
			resp["error"] = err.Error()
		} else {
			resp["error"] = firstFailureLine(clean)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePluginsInstalled returns the installed Claude Code plugins with the exact
// id + scope a delegated update needs. We do NOT attempt update DETECTION: the
// CLI's marketplace data does not expose a comparable version for these plugins
// (their `source` is a path, not a versioned ref), so "is an update available"
// can't be answered reliably. Per the user's choice this is a manual-update model
// (like skills.sh): offer update for each plugin, don't claim which need it.
// {available:false} on any failure so the UI offers nothing rather than guessing.
func (s *Server) handlePluginsInstalled(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cli, runner := s.claudePath, s.runner
	s.mu.Unlock()
	if cli == "" || runner == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 30*time.Second)
	defer cancel()
	stdout, _, err := runner.ListPlugins(ctx, cli)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	var list []installedPlugin
	if err := json.Unmarshal([]byte(extractJSON(stdout)), &list); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	// Determine which marketplaces are local (source "directory") so we only track
	// plugins from real remote marketplaces. Known-local names seed the set so a
	// failed/absent marketplace listing still excludes the obvious self-made ones.
	localMk := map[string]bool{"local": true, "skills-dir": true}
	if mkOut, _, mErr := runner.ListMarketplaces(ctx, cli); mErr == nil {
		var mks []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		}
		if json.Unmarshal([]byte(extractJSON(mkOut)), &mks) == nil {
			for _, m := range mks {
				if m.Source == "directory" {
					localMk[m.Name] = true
				}
			}
		}
	}
	type plug struct {
		Name  string `json:"name"`
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	out := []plug{}
	seen := map[string]bool{}
	for _, p := range list {
		name := pluginNameOf(p.ID)
		if name == "" || seen[name] {
			continue
		}
		// Only track plugins from a real (remote) marketplace — self-made local /
		// skills-dir plugins have no upstream to update from.
		if mk := pluginMarketplaceOf(p.ID); mk == "" || localMk[mk] {
			continue
		}
		seen[name] = true
		out = append(out, plug{Name: name, ID: p.ID, Scope: p.Scope})
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "plugins": out})
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

// originGuard is the CSRF gate for mutating handlers: it writes the uniform 403
// and reports false when the request is cross-origin, so callers can guard with
// `if !originGuard(w, r) { return }`. Returns true (writes nothing) when allowed.
func originGuard(w http.ResponseWriter, r *http.Request) bool {
	if !originLoopbackOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return false
	}
	return true
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
	ID          string `json:"id"`
	Root        string `json:"root"`        // absolute source root from /api/adoptable (relocate path)
	Src         string `json:"src"`         // plugin skill source dir (import path)
	Target      string `json:"target"`      // sync dir to map an imported plugin skill into
	Plugin      bool   `json:"plugin"`      // true → import-copy from a plugin tree, don't relocate
	Description string `json:"description"` // editable 简述 recorded in the 清单 (备份 always records name + 简述)
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
		writeErr(w, http.StatusBadRequest, "invalid", "unknown adopt root")
		return
	}
	if err := adopt.Adopt(req.ID, wantRoot, s.personalStore, mgr, &s.manifest); err != nil {
		code := "error"
		var ae *adopt.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		writeErr(w, adoptStatus(code), code, err.Error())
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
		writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	// 备份总要记录名称 + 简述到清单：优先用弹窗里编辑的简述，空则回退 SKILL.md。
	// 否则之后从 local 往 git 移动时会缺简述（commit 信息 / 清单定位）。
	desc := sanitizeCommitDesc(req.Description)
	if desc == "" {
		desc = skillDescription(filepath.Join(harness.Expand(s.personalStore), req.ID))
	}
	s.syncContrib(req.ID, desc, "local")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdoptPlugin imports a plugin skill (copy into the personal store, NOT
// relocate — the plugin cache is owned by the agent's plugin system) and maps
// the resulting @local/<id> into the chosen sync dir. Gated on the
// IncludePluginSkills opt-in; the source must live under a configured target's
// plugin tree (so this can't import arbitrary client paths).
// handleAddLocalSource registers a user-picked folder as a first-class local
// directory source (its own sidebar card). It does NOT copy: the skills are
// scanned live from the original folder and linked from there on enable/follow.
// The folder may itself be a skill (root SKILL.md) or a container of skill
// subdirectories — scanner.Scan handles both. Registering a source never links
// anything; the user then 启用 / 整仓跟随 via the "@dir:<id>" selector.
func (s *Server) handleAddLocalSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir   string `json:"dir"`
		Label string `json:"label"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Dir) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(req.Dir)
	dir := harness.Expand(raw)
	if harness.Guarded(dir) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该目录受保护，不能作为来源"})
		return
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录不存在或不是目录"})
		return
	}
	skills, err := scanner.Scan(dir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "扫描目录失败：" + err.Error()})
		return
	}
	if len(skills) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该目录里没有 SKILL.md：它既不是一个 skill，也不含 skill 子目录"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Refuse paths owned by skills.sh / the .agents directory source: those are
	// managed by `npx skills`, not a local source (would double-own / conflict).
	agentsRoot, _ := resolveAgentsLock(effectiveDirectorySources(s.cfg.DirectorySources))
	if ar := harness.Expand(agentsRoot); ar != "" && (dir == ar || strings.HasPrefix(dir, ar+string(filepath.Separator))) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该目录由 npx skills（skills.sh）管理，不能作为本地源——它已作为「npx skills」来源识别"})
		return
	}
	for _, d := range s.cfg.LocalSources {
		if harness.Expand(d.Path) == dir {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "该文件夹已经是本地源"})
			return
		}
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = filepath.Base(dir)
	}
	id := uniqueDirSourceID(s.cfg.LocalSources, label)
	s.cfg.LocalSources = append(s.cfg.LocalSources, config.DirectorySource{Path: raw, Label: label, ID: id})
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "label": label, "count": len(skills)})
}

// handleRemoveLocalSource unregisters a local directory source by id and tears
// down any links its skills had (its enabled entries are dropped, then reconcile
// removes the symlinks). The original folder is never touched.
func (s *Server) handleRemoveLocalSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.ID) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(req.ID)
	ns := reconcile.DirSelector(id)
	prefix := ns + "/"
	s.mu.Lock()
	kept := s.cfg.LocalSources[:0]
	found := false
	for _, d := range s.cfg.LocalSources {
		if d.ID == id {
			found = true
			continue
		}
		kept = append(kept, d)
	}
	if !found {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "未找到该本地源"})
		return
	}
	s.cfg.LocalSources = kept
	en := s.cfg.Enabled[:0]
	for _, e := range s.cfg.Enabled {
		if strings.HasPrefix(e.Skill, prefix) { // covers "@dir:<id>/*" and "@dir:<id>/<skill>"
			continue
		}
		en = append(en, e)
	}
	s.cfg.Enabled = en
	if err := s.persistConfigLocked(w); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.ReconcileOnly() // tear down this source's links now
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// uniqueDirSourceID derives a stable slug id from a label, deduped against the
// existing local sources (foo, foo-2, foo-3, …).
func uniqueDirSourceID(existing []config.DirectorySource, label string) string {
	base := pathutil.SanitizePathName(label)
	if base == "" {
		base = "src"
	}
	taken := func(id string) bool {
		for _, d := range existing {
			if d.ID == id {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d", base, n)
		if !taken(cand) {
			return cand
		}
	}
}

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
		writeErr(w, http.StatusBadRequest, "invalid", "plugin import is disabled")
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
		writeErr(w, http.StatusBadRequest, "invalid", "unknown target")
		return
	}
	if !srcAllowed {
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, "invalid", "source is not under a known plugin tree")
		return
	}
	if err := adopt.Import(src, req.ID, s.personalStore); err != nil {
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
	// Enabling a whole-source follow ("<ns>/*") subsumes any individual
	// "<ns>/<skill>" entries already enabled on the same target. Drop them so the
	// follow entry becomes the single owner of those links. Otherwise the two
	// coexist: canceling the follow later removes only "<ns>/*" and leaves the
	// individual entries (and their links) behind, forcing a one-by-one disable.
	if ns, ok := strings.CutSuffix(e.Skill, "/*"); ok {
		prefix := ns + "/"
		kept := s.cfg.Enabled[:0]
		for _, x := range s.cfg.Enabled {
			if x.Skill != e.Skill && strings.HasPrefix(x.Skill, prefix) && harness.Expand(x.Target) == wantTarget {
				continue // subsumed by the follow being added
			}
			kept = append(kept, x)
		}
		s.cfg.Enabled = kept
	} else if i := strings.LastIndexByte(e.Skill, '/'); i > 0 {
		// Conversely: adding an individual "<ns>/<skill>" when a whole-source
		// follow "<ns>/*" already covers this target is redundant — the follow
		// owns the link. Skip it, otherwise it lingers as a separate entry that
		// survives canceling the follow (then stays enabled when it shouldn't).
		followSel := e.Skill[:i] + "/*"
		for _, x := range s.cfg.Enabled {
			if x.Skill == followSel && harness.Expand(x.Target) == wantTarget {
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) // already covered by follow
				return
			}
		}
	}
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
	// A freshly-created project may have .claude (or .codex) but no skills/ child
	// yet, so SkillDirsFor would find nothing and fall back to the bare root. Fill
	// in the conventional skills/ under any agent home that already exists, so the
	// project can be added as a proper labeled target. Only existing homes are
	// touched — a project without .codex does not gain one.
	harness.ScaffoldSkillDirs(dir)
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

// handleSetTargetAlias renames a sync-target tab's display alias (dir → alias).
// An empty alias clears it (the tab falls back to showing the path). Used by the
// double-click-to-rename inline edit on tabs.
func (s *Server) handleSetTargetAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir   string `json:"dir"`
		Alias string `json:"alias"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Dir) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dir := strings.TrimSpace(req.Dir)
	alias := strings.TrimSpace(req.Alias)
	s.mu.Lock()
	defer s.mu.Unlock()
	known := false
	for _, d := range s.cfg.Targets {
		if d == dir {
			known = true
			break
		}
	}
	if !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown target"})
		return
	}
	if s.cfg.TargetAliases == nil {
		s.cfg.TargetAliases = map[string]string{}
	}
	if alias == "" {
		delete(s.cfg.TargetAliases, dir)
	} else {
		s.cfg.TargetAliases[dir] = alias
	}
	if err := s.persistConfigLocked(w); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// handleCheckUpdates contacts each cloned repo's remote (ls-remote, no object
// download) and records whether origin is ahead of the local mirror — so the
// user can see which repos have updates without auto-pulling (auto-update was
// removed). Network I/O runs unlocked; results merge into repoStatus.
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repos := append([]config.RepoConfig(nil), s.cfg.Repos...)
	noGit := s.syncer == nil
	gitErr := s.gitErr
	reposRoot := s.reposRoot
	syncer := s.syncer
	existing := make(map[string]RepoStatus, len(s.repoStatus))
	for k, v := range s.repoStatus {
		existing[k] = v
	}
	s.mu.Unlock()

	if noGit {
		writeJSON(w, http.StatusOK, map[string]any{"error": gitErr})
		return
	}
	ctx := s.detachedCtx()
	statuses := make(map[string]RepoStatus, len(repos))
	for _, repo := range repos {
		name := reconcile.RepoName(repo.URL)
		st := existing[repo.URL]
		st.URL, st.Branch, st.Name = repo.URL, repo.Branch, name
		dir := filepath.Join(reposRoot, name)
		if !dirHasContent(dir) {
			st.State, st.HasUpdate = "never-synced", false
			statuses[repo.URL] = st
			continue
		}
		if st.State == "" {
			st.State = "stale"
		}
		has, err := syncer.CheckUpdate(ctx, dir, repo.Branch)
		if err != nil {
			st.HasUpdate = false
			st.Error = err.Error()
			st.AuthHint = isAuthError(err.Error())
		} else {
			st.HasUpdate = has
			st.Error = ""
			st.AuthHint = false
			if !has {
				st.State = "synced" // confirmed local == remote → 已同步
			}
		}
		statuses[repo.URL] = st
	}
	updates := 0
	s.mu.Lock()
	for url, st := range statuses {
		s.repoStatus[url] = st
		if st.HasUpdate {
			updates++
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updates": updates})
}

// handleUpdateRepo fetches+reconciles a single repo (the per-repo「更新」button).
func (s *Server) handleUpdateRepo(w http.ResponseWriter, r *http.Request) {
	if !originGuard(w, r) {
		return
	}
	var req struct {
		URL   string `json:"url"`
		Force bool   `json:"force"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.URL) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(req.URL)
	sum := s.SyncOne(s.detachedCtx(), url, req.Force)
	// SyncOne records the git-sync outcome (including a dirty-skip) in repoStatus,
	// not in the reconcile summary. Surface the dirty flag so the UI can offer a
	// "discard local changes and update" (force) confirmation instead of silently
	// reporting success when the mirror was left untouched.
	s.mu.Lock()
	st := s.repoStatus[url]
	s.mu.Unlock()
	if st.State == string(gitsync.ActionFailed) || strings.TrimSpace(st.Error) != "" {
		log.Printf("update-repo %s: state=%s error=%s", url, st.State, st.Error)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": sum,
		"dirty":   st.Dirty && st.State == string(gitsync.ActionDirtySkip),
		"state":   st.State,
		"error":   st.Error,
		"drift":   st.Drift, // per-skill 新增/修改/已提交未推送 detail for the yield dialog
	})
}

// handleUpdateNow is the GIT update-all endpoint: it fetches + re-syncs every git
// source via SyncAll. npx/skills.sh is a separate concern with its own endpoint
// (handleUpdateSkillsShAll) — there is exactly one git update path and one npx
// update path, never a combined one. dirtyRepos lets the UI offer 还原并更新.
func (s *Server) handleUpdateNow(w http.ResponseWriter, r *http.Request) {
	var req updateReq
	_ = readJSON(r, &req) // empty body is fine (force defaults false)
	// Use the daemon-lifetime context, not the request's: closing the browser
	// tab mid-sync must not cancel in-flight git subprocesses (and leave repos
	// half-fetched), but daemon shutdown still cancels so the sync drains.
	sum := s.SyncAll(s.detachedCtx(), req.Force)

	// Collect repos left untouched because their mirror was dirty (force=false).
	// The UI uses this to offer a "还原并更新" (force) confirmation.
	s.mu.Lock()
	dirtyRepos := []string{}
	for _, repo := range s.cfg.Repos {
		if st, ok := s.repoStatus[repo.URL]; ok && st.Dirty && st.State == string(gitsync.ActionDirtySkip) {
			dirtyRepos = append(dirtyRepos, st.Name)
		}
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"created":    sum.Created,
		"removed":    sum.Removed,
		"pruned":     sum.Pruned,
		"conflicts":  sum.Conflicts,
		"errors":     sum.Errors,
		"dirtyRepos": dirtyRepos,
	})
}

// UpdateSkillsShAll delegates to `npx skills update` (no skill arg) to refresh
// every skills.sh-managed skill at once (invariant ④ — we trigger their native
// updater, never take ownership). ran=false means there was nothing to run (npx
// unavailable); when ran=true, err reports the command outcome (stderr folded in).
// Shared by the update-now endpoint and the daily scheduler.
func (s *Server) UpdateSkillsShAll(parent context.Context) (ran bool, err error) {
	s.mu.Lock()
	npx, runner := s.npxPath, s.runner
	s.mu.Unlock()
	if npx == "" || runner == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(parent, 180*time.Second)
	defer cancel()
	_, stderr, runErr := runner.UpdateAll(ctx, npx)
	if runErr != nil {
		if st := strings.TrimSpace(stderr); st != "" {
			return true, fmt.Errorf("%s", st)
		}
		return true, runErr
	}
	return true, nil
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
