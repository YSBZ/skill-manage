package server

import (
	"os"
	"testing"

	"skillmanage/internal/config"
)

func TestStaleEnabledSelfHeal(t *testing.T) {
	skipNoGit(t)
	s := newTestServer(t)
	if s.syncer == nil {
		t.Skip("no syncer")
	}
	if err := os.MkdirAll(s.reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	bareA := bareRemoteWithSeed(t, "repo-a")
	nameA, mirror := cloneMirrorWithSkill(t, s.reposRoot, bareA, "")
	seedNestedSkill(t, mirror, "real")
	tgt := t.TempDir()
	s.cfg.Repos = []config.RepoConfig{{URL: bareA}}
	// One real entry + one STALE entry (skill not in repo — like the user's leftover).
	s.cfg.Enabled = []config.EnabledEntry{
		{Skill: nameA + "/real", Target: tgt, Mode: config.ModeSnapshot},
		{Skill: nameA + "/mic-domain-lite-page", Target: tgt, Mode: config.ModeSnapshot},
	}
	sum := s.ReconcileOnly()
	t.Logf("errors=%v stale=%v", sum.Errors, sum.Stale)
	for _, e := range s.cfg.Enabled {
		t.Logf("enabled after heal: %q", e.Skill)
	}
	if len(sum.Errors) != 0 {
		t.Errorf("stale entry should NOT error, got %v", sum.Errors)
	}
	for _, e := range s.cfg.Enabled {
		if e.Skill == nameA+"/mic-domain-lite-page" {
			t.Errorf("stale entry not pruned")
		}
	}
}
