package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersonalTargets(t *testing.T) {
	t.Setenv("CODEX_HOME", "") // force tilde form
	ts := PersonalTargets()
	if len(ts) != 2 {
		t.Fatalf("want 2 personal targets, got %d", len(ts))
	}
	var cc, codex *Target
	for i := range ts {
		switch ts[i].Harness {
		case HarnessClaudeCode:
			cc = &ts[i]
		case HarnessCodex:
			codex = &ts[i]
		}
	}
	if cc == nil || cc.Dir != "~/.claude/skills/" || cc.Scope != ScopePersonal {
		t.Errorf("CC personal target wrong: %+v", cc)
	}
	if codex == nil || codex.Dir != "~/.codex/skills/" || codex.Scope != ScopePersonal {
		t.Errorf("Codex personal target wrong: %+v", codex)
	}
}

func TestCodexHomeOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	ts := PersonalTargets()
	want := filepath.Join(dir, "skills")
	found := false
	for _, x := range ts {
		if x.Harness == HarnessCodex && x.Dir == want {
			found = true
		}
	}
	if !found {
		t.Errorf("CODEX_HOME override should make Codex target %q, got %+v", want, ts)
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

func TestProjectTargets(t *testing.T) {
	proj := t.TempDir()
	// neither .codex/skills nor .agents/skills exists → default .codex/skills
	ts := ProjectTargets(proj)
	if len(ts) != 2 {
		t.Fatalf("want 2 project targets, got %d", len(ts))
	}
	var cc, codex *Target
	for i := range ts {
		switch ts[i].Harness {
		case HarnessClaudeCode:
			cc = &ts[i]
		case HarnessCodex:
			codex = &ts[i]
		}
	}
	if cc.Dir != proj+"/.claude/skills" {
		t.Errorf("CC project dir = %q", cc.Dir)
	}
	if codex.Dir != proj+"/.codex/skills" || codex.Ambiguous {
		t.Errorf("default Codex project should be .codex/skills, not ambiguous: %+v", codex)
	}
}

func TestProjectTargetsAgentsFallback(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := ProjectTargets(proj)
	for _, x := range ts {
		if x.Harness == HarnessCodex {
			if x.Dir != proj+"/.agents/skills" || x.Ambiguous {
				t.Errorf("only .agents/skills exists → expect it, not ambiguous: %+v", x)
			}
		}
	}
}

func TestProjectTargetsBothExistAmbiguous(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".codex", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := ProjectTargets(proj)
	for _, x := range ts {
		if x.Harness == HarnessCodex {
			if x.Dir != proj+"/.codex/skills" || !x.Ambiguous {
				t.Errorf("both exist → prefer .codex/skills AND flag ambiguous: %+v", x)
			}
		}
	}
}
