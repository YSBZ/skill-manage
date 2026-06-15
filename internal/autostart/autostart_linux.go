//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	markerBegin = "# >>> skillmanage autostart >>>"
	markerEnd   = "# <<< skillmanage autostart <<<"
)

// Manager registers via a guarded block appended to ~/.profile. On WSL with
// systemd a user unit is cleaner, but the profile hook is the universal
// least-privilege fallback.
type Manager struct {
	exePath     string
	profilePath string // ~/.profile (injectable for tests)
}

// New builds a linux/WSL autostart Manager for exePath.
func New(exePath string) (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Manager{exePath: exePath, profilePath: filepath.Join(home, ".profile")}, nil
}

func (m *Manager) block() string {
	return markerBegin + "\n" +
		`pgrep -x skillmanage >/dev/null 2>&1 || ( "` + m.exePath + `" --no-open >/dev/null 2>&1 & )` + "\n" +
		markerEnd + "\n"
}

// stripBlock returns content with any existing guarded block removed.
func stripBlock(content string) string {
	begin := strings.Index(content, markerBegin)
	if begin < 0 {
		return content
	}
	end := strings.Index(content, markerEnd)
	if end < 0 {
		return content
	}
	end += len(markerEnd)
	if end < len(content) && content[end] == '\n' {
		end++ // also consume the trailing newline after the end marker
	}
	return content[:begin] + content[end:]
}

func (m *Manager) read() string {
	b, err := os.ReadFile(m.profilePath)
	if err != nil {
		return ""
	}
	return string(b)
}

// Register writes the guarded launcher block, refreshing any existing one so
// the recorded path and args (e.g. --no-open) stay current. Idempotent.
func (m *Manager) Register() error {
	content := stripBlock(m.read())
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += m.block()
	return os.WriteFile(m.profilePath, []byte(content), 0o644)
}

// Unregister removes the guarded block.
func (m *Manager) Unregister() error {
	return os.WriteFile(m.profilePath, []byte(stripBlock(m.read())), 0o644)
}

// IsRegistered reports whether the guarded block is present.
func (m *Manager) IsRegistered() bool {
	return strings.Contains(m.read(), markerBegin)
}
