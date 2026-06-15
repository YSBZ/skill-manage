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
	// order preserved, harness inferred from path
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
		if ts[i].Dir != w.dir || ts[i].Harness != w.h {
			t.Errorf("target[%d] = %+v, want dir=%q harness=%q", i, ts[i], w.dir, w.h)
		}
	}
}

func TestTargetsEmpty(t *testing.T) {
	if ts := Targets(nil); ts != nil {
		t.Errorf("Targets(nil) should be nil, got %+v", ts)
	}
}

func TestClassify(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	codex := []string{"~/.codex/skills/", "/work/proj/.codex/skills", "/work/proj/.agents/skills"}
	for _, d := range codex {
		if Classify(d) != HarnessCodex {
			t.Errorf("Classify(%q) = %q, want codex", d, Classify(d))
		}
	}
	cc := []string{"~/.claude/skills/", "/work/proj/.claude/skills"}
	for _, d := range cc {
		if Classify(d) != HarnessClaudeCode {
			t.Errorf("Classify(%q) = %q, want cc", d, Classify(d))
		}
	}
	// neither cc nor codex → unknown, NOT defaulted to cc
	for _, d := range []string{"/tmp/whatever", "/opt/skills", "~/random/dir"} {
		if Classify(d) != HarnessUnknown {
			t.Errorf("Classify(%q) = %q, want unknown", d, Classify(d))
		}
	}
}

func TestDiscoverDefaultTargets(t *testing.T) {
	ch := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", ch)
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	want := filepath.Join(ch, "skills")
	// absent → not discovered
	if has(DiscoverDefaultTargets(), want) {
		t.Fatal("absent codex skills dir must not be discovered")
	}
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	// present → discovered (abs form under CODEX_HOME)
	if !has(DiscoverDefaultTargets(), want) {
		t.Errorf("existing codex skills dir should be discovered, got %v", DiscoverDefaultTargets())
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
