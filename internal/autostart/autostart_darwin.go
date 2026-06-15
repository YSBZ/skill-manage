//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Manager registers a per-user LaunchAgent (no root).
type Manager struct {
	exePath   string
	agentsDir string // ~/Library/LaunchAgents (injectable for tests)
}

// New builds a darwin autostart Manager for exePath.
func New(exePath string) (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Manager{exePath: exePath, agentsDir: filepath.Join(home, "Library", "LaunchAgents")}, nil
}

func (m *Manager) plistPath() string { return filepath.Join(m.agentsDir, label+".plist") }

func plistContent(exePath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array><string>` + exePath + `</string><string>--no-open</string></array>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
`
}

func (m *Manager) writePlist() error {
	if err := os.MkdirAll(m.agentsDir, 0o755); err != nil {
		return err
	}
	// launchd refuses group/other-writable plists.
	return os.WriteFile(m.plistPath(), []byte(plistContent(m.exePath)), 0o644)
}

func (m *Manager) removePlist() error {
	err := os.Remove(m.plistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Register writes the LaunchAgent plist and loads it (idempotent).
func (m *Manager) Register() error {
	if err := m.writePlist(); err != nil {
		return fmt.Errorf("write LaunchAgent: %w", err)
	}
	// Best-effort load via the modern launchctl API; ignore "already bootstrapped".
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootstrap", domain, m.plistPath()).Run()
	return nil
}

// Unregister unloads the LaunchAgent and removes its plist.
func (m *Manager) Unregister() error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+label).Run()
	return m.removePlist()
}

// IsRegistered reports whether the LaunchAgent plist exists.
func (m *Manager) IsRegistered() bool {
	_, err := os.Stat(m.plistPath())
	return err == nil
}
