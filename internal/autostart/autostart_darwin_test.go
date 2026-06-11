//go:build darwin

package autostart

import (
	"strings"
	"testing"
)

func TestDarwinPlistWriteAndDetect(t *testing.T) {
	m := &Manager{exePath: "/usr/local/bin/skillmanage", agentsDir: t.TempDir()}

	if m.IsRegistered() {
		t.Fatal("should not be registered before writing plist")
	}
	if err := m.writePlist(); err != nil {
		t.Fatalf("writePlist: %v", err)
	}
	if !m.IsRegistered() {
		t.Fatal("should be registered after writePlist")
	}

	content := plistContent(m.exePath)
	if !strings.Contains(content, "/usr/local/bin/skillmanage") {
		t.Error("plist should contain the executable path")
	}
	if !strings.Contains(content, "<key>RunAtLoad</key>") || !strings.Contains(content, "<true/>") {
		t.Error("plist should set RunAtLoad")
	}
	if !strings.Contains(content, label) {
		t.Error("plist should contain the label")
	}

	if err := m.removePlist(); err != nil {
		t.Fatalf("removePlist: %v", err)
	}
	if m.IsRegistered() {
		t.Error("should not be registered after removePlist")
	}
	// idempotent remove
	if err := m.removePlist(); err != nil {
		t.Errorf("removePlist on missing should be nil, got %v", err)
	}
}
