package server

import (
	"net/http/httptest"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/reconcile"
)

// TestFollowSubsumesIndividual reproduces the user-reported flow:
// single-enable @agents/A, then turn on whole-source follow @agents/*, then
// turn it off. Expectation: after unfollow, NOTHING remains enabled — the
// individual must have been subsumed by the follow, not left behind.
func TestFollowSubsumesIndividual(t *testing.T) {
	s := newTestServer(t)
	target := t.TempDir()
	ns := reconcile.AgentsNamespace

	add := func(skill, mode string) {
		w := httptest.NewRecorder()
		s.handleAddEnabled(w, req("POST", "/api/enabled", s.token, config.EnabledEntry{Skill: skill, Target: target, Mode: config.Mode(mode)}))
		if w.Code >= 400 {
			t.Fatalf("add %s: %d %s", skill, w.Code, w.Body.String())
		}
	}
	del := func(skill string) {
		w := httptest.NewRecorder()
		s.handleRemoveEnabled(w, req("DELETE", "/api/enabled", s.token, config.EnabledEntry{Skill: skill, Target: target}))
		if w.Code >= 400 {
			t.Fatalf("del %s: %d", skill, w.Code)
		}
	}
	count := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, e := range s.cfg.Enabled {
			if e.Target == target {
				n++
			}
		}
		return n
	}

	add(ns+"/A", "snapshot")
	if count() != 1 {
		t.Fatalf("after single-enable: %d entries, want 1", count())
	}
	add(ns+"/*", "follow") // 自动同步 on
	if count() != 1 {
		t.Fatalf("after follow: %d entries, want 1 (follow should subsume individual)", count())
	}
	del(ns + "/*") // 取消同步
	if count() != 0 {
		t.Errorf("after unfollow: %d entries, want 0 (individual must NOT survive)", count())
	}
}

// TestFollowThenIndividualIsRedundant covers the reverse order (the actual bug):
// follow is ON, then an individual is added (e.g. installing a skill while
// auto-syncing). The individual must NOT create a separate entry — it's covered
// by follow — so canceling follow leaves nothing enabled.
func TestFollowThenIndividualIsRedundant(t *testing.T) {
	s := newTestServer(t)
	target := t.TempDir()
	ns := reconcile.AgentsNamespace
	add := func(skill, mode string) {
		w := httptest.NewRecorder()
		s.handleAddEnabled(w, req("POST", "/api/enabled", s.token, config.EnabledEntry{Skill: skill, Target: target, Mode: config.Mode(mode)}))
		if w.Code >= 400 {
			t.Fatalf("add %s: %d", skill, w.Code)
		}
	}
	count := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, e := range s.cfg.Enabled {
			if e.Target == target {
				n++
			}
		}
		return n
	}

	add(ns+"/*", "follow")     // 自动同步 on
	add(ns+"/B", "snapshot")   // 同步中安装个体 → 应被视为冗余，不新增
	if count() != 1 {
		t.Fatalf("follow + individual: %d entries, want 1 (individual redundant under follow)", count())
	}
	// 取消同步 → 应全清
	w := httptest.NewRecorder()
	s.handleRemoveEnabled(w, req("DELETE", "/api/enabled", s.token, config.EnabledEntry{Skill: ns + "/*", Target: target}))
	if count() != 0 {
		t.Errorf("after unfollow: %d entries, want 0", count())
	}
}
