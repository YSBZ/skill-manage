package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTargets(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home, _ := os.UserHomeDir()
	dirs := []string{
		"~/.claude/skills/",         // cc
		"~/.codex/skills/",          // codex
		"/work/proj/.codex/skills",  // codex (project path, no taxonomy)
		"/work/proj/.claude/skills", // cc
		"  ",                        // blank → dropped
		filepath.Join(home, ".codex", "skills", ".system"), // guarded → dropped
	}
	ts := Targets(dirs)
	if len(ts) != 4 {
		t.Fatalf("want 4 targets (blank + guarded dropped), got %d: %+v", len(ts), ts)
	}
	// order preserved, harness inferred from path, label == harness
	want := []struct {
		dir string
		h   Harness
	}{
		{"~/.claude/skills/", HarnessClaudeCode},
		{"~/.codex/skills/", HarnessCodex},
		{"/work/proj/.codex/skills", HarnessCodex},
		{"/work/proj/.claude/skills", HarnessClaudeCode},
	}
	for i, w := range want {
		if ts[i].Dir != w.dir || ts[i].Harness != w.h || ts[i].Label != string(w.h) {
			t.Errorf("target[%d] = %+v, want dir=%q harness=%q", i, ts[i], w.dir, w.h)
		}
	}
}

func TestTargetsEmpty(t *testing.T) {
	if ts := Targets(nil); ts != nil {
		t.Errorf("Targets(nil) should be nil, got %+v", ts)
	}
}

func TestGuarded(t *testing.T) {
	home, _ := os.UserHomeDir()
	codexSkills := filepath.Join(home, ".codex", "skills")
	t.Setenv("CODEX_HOME", "")
	guarded := []string{
		filepath.Join(codexSkills, ".system"),
		filepath.Join(codexSkills, ".system", "skill-creator"),
		filepath.Join(home, ".codex", "vendor_imports", "skills"),
		filepath.Join(home, ".codex", "vendor_imports", "skills", "x"),
		"~/.codex/skills/.system", // tilde form must resolve
	}
	for _, g := range guarded {
		if !Guarded(g) {
			t.Errorf("Guarded(%q) = false, want true", g)
		}
	}
	notGuarded := []string{
		"~/.codex/skills/",
		filepath.Join(codexSkills, "my-skill"),
		"~/.claude/skills/",
		filepath.Join(home, ".codex", "skills-extra"), // sibling prefix, not under .system
	}
	for _, n := range notGuarded {
		if Guarded(n) {
			t.Errorf("Guarded(%q) = true, want false", n)
		}
	}
}

func TestGuardedCodexHomeOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	if !Guarded(filepath.Join(dir, "skills", ".system")) {
		t.Errorf("CODEX_HOME .system should be guarded")
	}
	if Guarded(filepath.Join(dir, "skills", "my-skill")) {
		t.Errorf("a normal skill under CODEX_HOME must not be guarded")
	}
}

func TestIsCodexTarget(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	codex := []string{"~/.codex/skills/", "~/.codex/skills/foo", "/home/u/proj/.codex/skills", "/home/u/proj/.agents/skills"}
	for _, c := range codex {
		if !IsCodexTarget(c) {
			t.Errorf("IsCodexTarget(%q) = false, want true", c)
		}
	}
	cc := []string{"~/.claude/skills/", "/home/u/proj/.claude/skills"}
	for _, c := range cc {
		if IsCodexTarget(c) {
			t.Errorf("IsCodexTarget(%q) = true, want false (CC target)", c)
		}
	}
}

func TestExpandTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := Expand("~/.claude/skills/"); got != filepath.Join(home, ".claude", "skills") {
		t.Errorf("Expand tilde = %q", got)
	}
}
