// Package server hosts the embedded single-page UI and the token-authenticated
// REST API that drives SkillManage (U8). It also owns the in-memory config and
// ownership manifest, and the SyncAll orchestration the scheduler reuses.
package server

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
	"skillmanage/internal/harness"
	"skillmanage/internal/reconcile"
)

const tokenPlaceholder = "__SM_TOKEN__"

// RepoStatus is the last-known sync state of one tracked repo (R7).
type RepoStatus struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Dirty  bool   `json:"dirty,omitempty"`
	Error  string `json:"error,omitempty"`
	// AuthHint is set when Error looks like a credentials/key failure, so the UI
	// can tell the user how to fix auto-update instead of showing a raw git error.
	AuthHint bool `json:"authHint,omitempty"`
	// HasUpdate is set by the on-demand 检查更新 endpoint when origin is ahead of
	// the local mirror — a hint to run 立即更新. Cleared after a sync.
	HasUpdate bool `json:"hasUpdate,omitempty"`
}

// isAuthError reports whether a git error message indicates a credentials/key
// failure (as opposed to a network or missing-repo error). Auto-update runs
// non-interactively, so a private repo without configured credentials fails
// here rather than prompting — and the user needs a pointer to fix it.
func isAuthError(msg string) bool {
	m := strings.ToLower(msg)
	for _, sig := range []string{
		"authentication failed",
		"could not read username",
		"could not read password",
		"terminal prompts disabled", // GIT_TERMINAL_PROMPT=0 hit a creds prompt
		"permission denied",         // ssh publickey
		"publickey",
		"host key verification failed",
		"invalid username or password",
		"http basic",                            // remote: HTTP Basic: Access denied
		"could not read from remote repository", // common ssh auth tail
		"returned error: 403",
		"returned error: 401",
	} {
		if strings.Contains(m, sig) {
			return true
		}
	}
	return false
}

// Server is the daemon's HTTP surface and shared state.
type Server struct {
	centralDir    string
	reposRoot     string
	personalStore string
	token         string
	uiFS          fs.FS
	indexHTML     []byte // token already injected

	syncer     *gitsync.Syncer
	gitErr     string // non-empty when git is unavailable; syncer is then nil
	reconciler *reconcile.Reconciler

	// npxPath is the resolved `npx` executable ("" when not installed); when
	// present the UI offers a one-click skills.sh update (U7). runner executes it
	// (platform default; injectable in tests).
	npxPath string
	// claudePath is the resolved `claude` CLI ("" when not installed); when present
	// the UI offers a delegated plugin update (`claude plugin update <name>`). We
	// never take ownership of plugins (invariant ④) — this only invokes the
	// harness's own update command.
	claudePath string
	runner     skillsRunner

	// baseCtx is the daemon-lifetime context used as the parent for syncs that
	// must outlive the originating HTTP request (update-now: closing the tab
	// must not abort the sync) yet still cancel on daemon shutdown. Set once via
	// SetBaseContext before serving; nil means context.Background().
	baseCtx context.Context
	// syncWG tracks in-flight SyncAll calls so shutdown can drain them before
	// the lock is released and the git hooks dir is removed (reliability).
	syncWG sync.WaitGroup

	mu          sync.Mutex
	cfg         config.Config
	manifest    config.Manifest
	firstRun    bool
	repoStatus  map[string]RepoStatus
	lastSummary reconcile.Summary
}

// SetBaseContext sets the daemon-lifetime parent context for request-detached
// syncs. Call once before serving.
func (s *Server) SetBaseContext(ctx context.Context) { s.baseCtx = ctx }

// detachedCtx returns the daemon-lifetime context (or Background if unset). It
// is detached from any HTTP request so a sync survives the request that started
// it, while still cancelling on daemon shutdown.
func (s *Server) detachedCtx() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// WaitForSyncs blocks until all in-flight SyncAll calls return. The daemon
// calls it on shutdown — after the base context is cancelled — so the lock and
// git hooks dir are not torn down while git subprocesses are still running.
func (s *Server) WaitForSyncs() { s.syncWG.Wait() }

// Schedule returns the configured daily time ("HH:MM"), defaulting to 09:00.
func (s *Server) Schedule() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Schedule.DailyAt == "" {
		return defaultDailyAt
	}
	return s.cfg.Schedule.DailyAt
}

// defaultDailyAt is the fallback daily-sync time when config carries none.
const defaultDailyAt = "09:40"

// RunScheduler runs the daily update loop until ctx is cancelled. Each day at the
// configured wall-clock time (Schedule(), local) it performs a full update: pull
// git sources (SyncAll) AND delegate to `npx skills update` for skills.sh sources
// — the same coverage as the「全量更新」button. The schedule is re-read every
// cycle so a config change takes effect on the next fire without a restart.
func (s *Server) RunScheduler(ctx context.Context) {
	for {
		next := nextDailyFire(time.Now(), s.Schedule())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		// Detached from any single request; still cancels on daemon shutdown.
		s.SyncAll(ctx, false)
		if _, err := s.UpdateSkillsShAll(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "skillmanage: scheduled npx skills update failed:", err)
		}
	}
}

// nextDailyFire returns the next local time strictly after `now` matching the
// "HH:MM" daily time. A malformed/empty spec falls back to defaultDailyAt.
func nextDailyFire(now time.Time, hhmm string) time.Time {
	h, m, ok := parseHHMM(hhmm)
	if !ok {
		h, m, _ = parseHHMM(defaultDailyAt)
	}
	fire := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !fire.After(now) {
		fire = fire.Add(24 * time.Hour)
	}
	return fire
}

// parseHHMM parses a "HH:MM" 24-hour time. ok=false on any malformed input.
func parseHHMM(s string) (hour, min int, ok bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// New builds a Server rooted at centralDir.
func New(centralDir string) (*Server, error) {
	token, err := LoadOrCreateToken(centralDir)
	if err != nil {
		return nil, err
	}
	cfg, firstRun, err := config.LoadConfig(centralDir)
	if err != nil {
		return nil, err
	}
	manifest, err := config.LoadManifest(centralDir)
	if err != nil {
		return nil, err
	}
	reposRoot := filepath.Join(centralDir, "repos")
	personalStore := config.PersonalStorePath(centralDir)
	// Seed sync targets by DISCOVERY, never hardcoded: on a config that has never
	// configured targets (nil), probe the default agent install dirs and add only
	// the ones that actually exist. If none exist, leave it unset so the user
	// adds dirs manually (and a later start can still discover newly-installed
	// agents).
	seeded := false
	if cfg.Targets == nil {
		if dirs := harness.DiscoverDefaultTargets(); len(dirs) > 0 {
			cfg.Targets = dirs
			seeded = true
		}
	}
	// Heal adopt links written before adoption recorded an enabled entry, so the
	// reconcile orphan pass does not delete them on the next sync (data stays in
	// the store, but the in-place symlink would otherwise vanish).
	if backfillAdoptedEnabled(&cfg, &manifest, personalStore) || seeded {
		if err := config.SaveConfig(centralDir, cfg); err != nil {
			return nil, fmt.Errorf("persist target seed / adopted-skill backfill: %w", err)
		}
	}
	uiFS, err := UIFS()
	if err != nil {
		return nil, err
	}
	rawIndex, err := fs.ReadFile(uiFS, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded index.html: %w", err)
	}
	// git absence must NOT kill startup — otherwise the windowless Windows build
	// just dies on a machine without Git installed ("闪一下就关了"). Bring the UI
	// up regardless and surface the problem; syncs no-op until git is present.
	syncer, gitErr := gitsync.NewSyncer()
	gitErrMsg := ""
	if gitErr != nil {
		gitErrMsg = gitErr.Error()
		syncer = nil
	} else if exe, e := os.Executable(); e == nil {
		// Wire the daemon's own binary as git's credential helper so stored
		// HTTPS PATs feed auto-update fetches (GIT_ASKPASS).
		syncer.SetAskpass(exe, centralDir)
	}
	npxPath, _ := exec.LookPath("npx")       // "" when not installed → UI hides one-click update
	claudePath, _ := exec.LookPath("claude") // "" → UI hides delegated plugin update
	return &Server{
		centralDir:    centralDir,
		reposRoot:     reposRoot,
		personalStore: personalStore,
		token:         token,
		uiFS:          uiFS,
		indexHTML:     bustAssetCache(bytes.ReplaceAll(rawIndex, []byte(tokenPlaceholder), []byte(token)), token),
		syncer:        syncer,
		gitErr:        gitErrMsg,
		reconciler:    reconcile.New(reposRoot, personalStore),
		npxPath:       npxPath,
		claudePath:    claudePath,
		runner:        defaultSkillsRunner(),
		cfg:           cfg,
		manifest:      manifest,
		firstRun:      firstRun,
		repoStatus:    map[string]RepoStatus{},
	}, nil
}

// backfillAdoptedEnabled protects already-adopted skills written before adoption
// recorded an enabled entry. Every manifest link whose source sits directly in
// the personal store is an in-place adopt link; if no enabled entry maps it,
// reconcile's orphan pass deletes it on the next sync. Add the missing
// @local/<name> → <target> mapping. Returns true if cfg was modified.
func backfillAdoptedEnabled(cfg *config.Config, manifest *config.Manifest, personalStore string) bool {
	storeAbs := harness.Expand(personalStore)
	// Map an expanded dir back to its configured (user-facing) form, e.g.
	// "/Users/x/.claude/skills" → "~/.claude/skills/". The UI matches an enabled
	// entry's Target against the dropdown value (a config.Targets string) by exact
	// string compare, so entries must carry that exact form or the checkbox state
	// disagrees with the actual link.
	canon := map[string]string{}
	for _, d := range cfg.Targets {
		canon[harness.Expand(d)] = d
	}
	canonicalize := func(target string) string {
		if c, ok := canon[harness.Expand(target)]; ok {
			return c
		}
		return target
	}
	changed := false
	// 1. normalize existing enabled targets to the configured form (heals entries
	//    written in absolute form by an earlier backfill).
	for i := range cfg.Enabled {
		if c := canonicalize(cfg.Enabled[i].Target); c != cfg.Enabled[i].Target {
			cfg.Enabled[i].Target = c
			changed = true
		}
	}
	// 2. backfill missing @local mappings for store-sourced manifest links.
	have := map[string]bool{}
	for _, e := range cfg.Enabled {
		have[e.Skill+"\x00"+harness.Expand(e.Target)] = true
	}
	for _, l := range manifest.Links {
		if filepath.Dir(harness.Expand(l.Source)) != storeAbs {
			continue // not an adopted (store-sourced) link
		}
		skill := reconcile.LocalNamespace + "/" + l.Name
		key := skill + "\x00" + harness.Expand(l.Target)
		if have[key] {
			continue
		}
		cfg.Enabled = append(cfg.Enabled, config.EnabledEntry{Skill: skill, Target: canonicalize(l.Target), Mode: config.ModeSnapshot})
		have[key] = true
		changed = true
	}
	return changed
}

// Close releases resources.
func (s *Server) Close() error {
	if s.syncer != nil {
		return s.syncer.Close()
	}
	return nil
}

// SyncAll mirrors every tracked repo then reconciles links. force discards
// local drift (R5 mirror semantics); when false, dirty repos are surfaced and
// left untouched (R26). The scheduler and the update-now endpoint both call it.
func (s *Server) SyncAll(ctx context.Context, force bool) reconcile.Summary {
	s.syncWG.Add(1)
	defer s.syncWG.Done()
	// Snapshot the repo list under a short lock, then run the git network I/O
	// UNLOCKED so a slow/hung fetch never blocks the UI or other handlers.
	s.mu.Lock()
	repos := append([]config.RepoConfig(nil), s.cfg.Repos...)
	noGit := s.syncer == nil
	gitErr := s.gitErr
	s.mu.Unlock()

	statuses := make(map[string]RepoStatus, len(repos))
	for _, repo := range repos {
		// Without git we can't fetch; record the reason on each repo and skip the
		// network step. Reconcile below still links any mirrors already on disk.
		if noGit {
			name := reconcile.RepoName(repo.URL)
			statuses[repo.URL] = RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: name, State: "failed", Error: gitErr}
			continue
		}
		name := reconcile.RepoName(repo.URL)
		dir := filepath.Join(s.reposRoot, name)
		res := s.syncer.Sync(ctx, dir, repo.URL, gitsync.Options{Branch: repo.Branch, Force: force})
		st := RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: name, State: string(res.Action), Dirty: res.Dirty}
		if res.Err != nil {
			// Surface git's actual stderr (the useful part) — res.Err alone is just
			// "exit status 128". Both feed auth detection.
			st.Error = res.Err.Error()
			if s := strings.TrimSpace(res.Stderr); s != "" {
				st.Error = s
			}
			st.AuthHint = isAuthError(res.Stderr + " " + res.Err.Error())
		}
		statuses[repo.URL] = st
	}

	// Re-acquire to apply reconcile and write state (fast, no network I/O).
	s.mu.Lock()
	defer s.mu.Unlock()
	for url, st := range statuses {
		s.repoStatus[url] = st
	}
	s.reconciler.SetAgentsRoot(s.agentsRootLocked())
	s.reconciler.SetDirSources(s.dirSourceMapLocked())
	sum := s.reconciler.Apply(s.cfg, &s.manifest)
	if err := config.SaveManifest(s.centralDir, s.manifest); err != nil {
		sum.Errors = append(sum.Errors, fmt.Sprintf("save manifest: %v", err))
	}
	s.lastSummary = sum
	return sum
}

// SyncOne fetches a single repo (by URL) and reconciles. Mirrors SyncAll but
// scoped to one repo — backs the per-repo「更新」button. Network I/O runs
// unlocked; state write is under the lock.
func (s *Server) SyncOne(ctx context.Context, url string, force bool) reconcile.Summary {
	s.syncWG.Add(1)
	defer s.syncWG.Done()
	s.mu.Lock()
	var target *config.RepoConfig
	for i := range s.cfg.Repos {
		if s.cfg.Repos[i].URL == url {
			target = &s.cfg.Repos[i]
			break
		}
	}
	noGit := s.syncer == nil
	gitErr := s.gitErr
	reposRoot := s.reposRoot
	s.mu.Unlock()
	if target == nil {
		return reconcile.Summary{Errors: []string{"unknown repo: " + url}}
	}
	repo := *target
	name := reconcile.RepoName(repo.URL)
	var st RepoStatus
	if noGit {
		st = RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: name, State: "failed", Error: gitErr}
	} else {
		dir := filepath.Join(reposRoot, name)
		res := s.syncer.Sync(ctx, dir, repo.URL, gitsync.Options{Branch: repo.Branch, Force: force})
		st = RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: name, State: string(res.Action), Dirty: res.Dirty}
		if res.Err != nil {
			st.Error = res.Err.Error()
			if e := strings.TrimSpace(res.Stderr); e != "" {
				st.Error = e
			}
			st.AuthHint = isAuthError(res.Stderr + " " + res.Err.Error())
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repoStatus[repo.URL] = st
	s.reconciler.SetAgentsRoot(s.agentsRootLocked())
	s.reconciler.SetDirSources(s.dirSourceMapLocked())
	sum := s.reconciler.Apply(s.cfg, &s.manifest)
	if err := config.SaveManifest(s.centralDir, s.manifest); err != nil {
		sum.Errors = append(sum.Errors, fmt.Sprintf("save manifest: %v", err))
	}
	s.lastSummary = sum
	return sum
}

// ReconcileOnly re-applies links from the current config without pulling git.
// The UI calls it after a selection change so links materialize immediately
// without waiting for a full sync.
func (s *Server) ReconcileOnly() reconcile.Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciler.SetAgentsRoot(s.agentsRootLocked())
	s.reconciler.SetDirSources(s.dirSourceMapLocked())
	sum := s.reconciler.Apply(s.cfg, &s.manifest)
	if err := config.SaveManifest(s.centralDir, s.manifest); err != nil {
		sum.Errors = append(sum.Errors, fmt.Sprintf("save manifest: %v", err))
	}
	s.lastSummary = sum
	return sum
}

// Bind listens on loopback at preferredPort, falling back to an OS-assigned
// free port if it is taken (R21). The returned listener is kept open (no
// close-then-reopen race).
func (s *Server) Bind(preferredPort int) (net.Listener, error) {
	if preferredPort > 0 {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
		if err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// WriteAddress records the resolved UI address so an autostart-launched,
// terminal-less daemon is still discoverable (KTD8).
func (s *Server) WriteAddress(addr string) error {
	url := "http://" + addr + "/"
	return os.WriteFile(config.AddressPath(s.centralDir), []byte(url), 0o644)
}

// spaHandler serves embedded assets, falling back to the token-injected
// index.html for client-side routes.
func (s *Server) spaHandler() http.HandlerFunc {
	fileServer := http.FileServerFS(s.uiFS)
	return func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.ToSlash(filepath.Clean(r.URL.Path))
		name := clean
		for len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		if name == "" || name == "index.html" {
			s.serveIndex(w)
			return
		}
		if _, err := fs.Stat(s.uiFS, name); err != nil {
			// not a real asset → SPA entry point
			s.serveIndex(w)
			return
		}
		// 本地单机工具、资源随二进制内嵌：禁用浏览器缓存，避免重新构建后
		// 浏览器仍加载旧 app.js/app.css（「改了不生效」的根因）。
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		fileServer.ServeHTTP(w, r)
	}
}

// bustAssetCache appends a per-process version query to the embedded asset URLs
// so a rebuilt/restarted daemon never serves a UI that pairs new index.html with
// a browser-cached old app.js/app.css. The token already varies每次启动.
func bustAssetCache(html []byte, version string) []byte {
	html = bytes.ReplaceAll(html, []byte(`href="/app.css"`), []byte(`href="/app.css?v=`+version+`"`))
	html = bytes.ReplaceAll(html, []byte(`src="/app.js"`), []byte(`src="/app.js?v=`+version+`"`))
	return html
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_, _ = w.Write(s.indexHTML)
}
