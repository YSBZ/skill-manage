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
	"sync"

	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
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
}

// Server is the daemon's HTTP surface and shared state.
type Server struct {
	centralDir string
	reposRoot  string
	token      string
	uiFS       fs.FS
	indexHTML  []byte // token already injected

	syncer     *gitsync.Syncer
	reconciler *reconcile.Reconciler

	mu          sync.Mutex
	cfg         config.Config
	manifest    config.Manifest
	firstRun    bool
	repoStatus  map[string]RepoStatus
	lastSummary reconcile.Summary
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
	uiFS, err := UIFS()
	if err != nil {
		return nil, err
	}
	rawIndex, err := fs.ReadFile(uiFS, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded index.html: %w", err)
	}
	syncer, err := gitsync.NewSyncer()
	if err != nil {
		return nil, err
	}
	reposRoot := filepath.Join(centralDir, "repos")
	return &Server{
		centralDir: centralDir,
		reposRoot:  reposRoot,
		token:      token,
		uiFS:       uiFS,
		indexHTML:  bytes.ReplaceAll(rawIndex, []byte(tokenPlaceholder), []byte(token)),
		syncer:     syncer,
		reconciler: reconcile.New(reposRoot),
		cfg:        cfg,
		manifest:   manifest,
		firstRun:   firstRun,
		repoStatus: map[string]RepoStatus{},
	}, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, repo := range s.cfg.Repos {
		name := reconcile.RepoName(repo.URL)
		dir := filepath.Join(s.reposRoot, name)
		res := s.syncer.Sync(ctx, dir, repo.URL, gitsync.Options{Branch: repo.Branch, Force: force})
		st := RepoStatus{URL: repo.URL, Branch: repo.Branch, Name: name, State: string(res.Action), Dirty: res.Dirty}
		if res.Err != nil {
			st.Error = res.Err.Error()
		}
		s.repoStatus[repo.URL] = st
	}

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
