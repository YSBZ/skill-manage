package harness

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSkillDirsFor(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	has := func(list []string, suffix string) bool {
		for _, x := range list {
			if strings.HasSuffix(x, suffix) {
				return true
			}
		}
		return false
	}

	// a project root holding BOTH cc and codex skill dirs → both detected
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".codex", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := SkillDirsFor(proj)
	if len(got) != 2 || !has(got, filepath.Join(".claude", "skills")) || !has(got, filepath.Join(".codex", "skills")) {
		t.Errorf("project root should fan out to cc+codex skills dirs, got %v", got)
	}

	// a ".claude home" → its skills/ child is the cc dir
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = SkillDirsFor(filepath.Join(home, ".claude"))
	if len(got) != 1 || !has(got, filepath.Join(".claude", "skills")) {
		t.Errorf(".claude home should yield its skills child, got %v", got)
	}

	// a dir with no cc/codex skill dirs → empty (caller falls back to verbatim)
	if got := SkillDirsFor(t.TempDir()); len(got) != 0 {
		t.Errorf("plain dir should yield nothing, got %v", got)
	}
}

func TestScaffoldSkillDirs(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	exists := func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

	// project root with .claude (no skills/) and .codex (no skills/) → both filled,
	// and SkillDirsFor now fans out to both because the children exist.
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	created := ScaffoldSkillDirs(proj)
	if len(created) != 2 {
		t.Fatalf("want 2 created skills dirs, got %v", created)
	}
	if !exists(filepath.Join(proj, ".claude", "skills")) || !exists(filepath.Join(proj, ".codex", "skills")) {
		t.Errorf("skills/ should now exist under both homes")
	}
	if got := SkillDirsFor(proj); len(got) != 2 {
		t.Errorf("after scaffold SkillDirsFor should fan out to 2, got %v", got)
	}

	// absent home is NOT created: a project with only .claude must not gain .codex.
	only := t.TempDir()
	if err := os.MkdirAll(filepath.Join(only, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	ScaffoldSkillDirs(only)
	if exists(filepath.Join(only, ".codex")) {
		t.Errorf(".codex must not be created when absent")
	}
	if !exists(filepath.Join(only, ".claude", "skills")) {
		t.Errorf(".claude/skills should be created")
	}

	// idempotent: an existing skills/ is left alone (nothing created on 2nd pass).
	if c := ScaffoldSkillDirs(only); len(c) != 0 {
		t.Errorf("second pass should create nothing, got %v", c)
	}

	// picking the .claude home directly also fills its skills/ child.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if c := ScaffoldSkillDirs(filepath.Join(home, ".claude")); len(c) != 1 {
		t.Errorf("picking .claude home should create its skills child, got %v", c)
	}

	// plain project with no agent home → nothing created.
	if c := ScaffoldSkillDirs(t.TempDir()); len(c) != 0 {
		t.Errorf("plain dir should scaffold nothing, got %v", c)
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
