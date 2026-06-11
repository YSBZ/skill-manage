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
		`pgrep -x skillmanage >/dev/null 2>&1 || ( "` + m.exePath + `" >/dev/null 2>&1 & )` + "\n" +
		markerEnd + "\n"
}

func (m *Manager) read() string {
	b, err := os.ReadFile(m.profilePath)
	if err != nil {
		return ""
	}
	return string(b)
}

// Register appends the guarded launcher block (idempotent).
func (m *Manager) Register() error {
	content := m.read()
	if strings.Contains(content, markerBegin) {
		return nil // already registered
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += m.block()
	return os.WriteFile(m.profilePath, []byte(content), 0o644)
}

// Unregister removes the guarded block.
func (m *Manager) Unregister() error {
	content := m.read()
	begin := strings.Index(content, markerBegin)
	if begin < 0 {
		return nil
	}
	end := strings.Index(content, markerEnd)
	if end < 0 {
		return nil
	}
	end += len(markerEnd)
	// also consume a trailing newline after the end marker
	if end < len(content) && content[end] == '\n' {
		end++
	}
	out := content[:begin] + content[end:]
	return os.WriteFile(m.profilePath, []byte(out), 0o644)
}

// IsRegistered reports whether the guarded block is present.
func (m *Manager) IsRegistered() bool {
	return strings.Contains(m.read(), markerBegin)
}
