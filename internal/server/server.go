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

	autostart AutostartManager

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

// AutostartManager registers/unregisters the daemon for login start (R19). The
// concrete implementation is platform-specific (internal/autostart); the server
// depends on the interface so it stays testable without touching the OS.
type AutostartManager interface {
	Register() error
	Unregister() error
	IsRegistered() bool
}

// SetAutostart wires in the platform autostart manager (called from main).
func (s *Server) SetAutostart(m AutostartManager) { s.autostart = m }

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
		return "09:00"
	}
	return s.cfg.Schedule.DailyAt
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
	return &Server{
		centralDir:    centralDir,
		reposRoot:     reposRoot,
		personalStore: personalStore,
		token:         token,
		uiFS:          uiFS,
		indexHTML:     bytes.ReplaceAll(rawIndex, []byte(tokenPlaceholder), []byte(token)),
		syncer:        syncer,
		gitErr:        gitErrMsg,
		reconciler:    reconcile.New(reposRoot, personalStore),
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
	sum := s.reconciler.Apply(s.cfg, &s.manifest)
	if err := config.SaveManifest(s.centralDir, s.manifest); err != nil {
		sum.Errors = append(sum.Errors, fmt.Sprintf("save manifest: %v", err))
	}
	s.lastSummary = sum
	_ = os.WriteFile(config.LastSyncPath(s.centralDir), []byte(time.Now().Format(time.RFC3339)), 0o644)
	return sum
}

// LastSync returns the timestamp of the last successful sync, or the zero time
// if none is recorded — used by the scheduler's startup missed-run check.
func (s *Server) LastSync() time.Time {
	b, err := os.ReadFile(config.LastSyncPath(s.centralDir))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, string(bytes.TrimSpace(b)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// ReconcileOnly re-applies links from the current config without pulling git.
// The UI calls it after a selection change so links materialize immediately
// without waiting for a full sync.
func (s *Server) ReconcileOnly() reconcile.Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		fileServer.ServeHTTP(w, r)
	}
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.indexHTML)
}
