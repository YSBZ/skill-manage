package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"skillmanage/internal/config"
)

// LoadOrCreateToken returns the API bearer token for centralDir, generating and
// persisting one (0600) on first use (KTD11/R23).
func LoadOrCreateToken(centralDir string) (string, error) {
	path := config.TokenPath(centralDir)
	if b, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	if err := os.MkdirAll(centralDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

// hostAllowed reports whether the request Host header resolves to loopback,
// blocking DNS-rebinding from a browser page (R23). Only localhost / 127.0.0.1
// / ::1 are accepted; the port is irrelevant.
func hostAllowed(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// requireAuth wraps an API handler with Host-header validation and bearer-token
// auth (KTD11). The SPA/asset routes do not use this — they are the bootstrap
// that carries the token to the client — but they still pass through hostGuard.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// hostGuard wraps non-API routes (the SPA) with only the Host-header check.
func (s *Server) hostGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
