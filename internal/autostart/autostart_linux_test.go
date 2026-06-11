//go:build linux

package autostart

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxProfileRegisterUnregister(t *testing.T) {
	profile := filepath.Join(t.TempDir(), ".profile")
	m := &Manager{exePath: "/home/u/bin/skillmanage", profilePath: profile}

	if m.IsRegistered() {
		t.Fatal("should not be registered initially")
	}
	if err := m.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !m.IsRegistered() {
		t.Fatal("should be registered after Register")
	}
	if !strings.Contains(m.read(), "/home/u/bin/skillmanage") {
		t.Error("profile block should reference the executable")
	}
	// idempotent register: no duplicate block
	_ = m.Register()
	if strings.Count(m.read(), markerBegin) != 1 {
		t.Error("Register should be idempotent (one block)")
	}
	if err := m.Unregister(); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if m.IsRegistered() {
		t.Error("should not be registered after Unregister")
	}
}
