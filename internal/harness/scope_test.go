package harness

import (
	"path/filepath"
	"testing"
)

func TestScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "") // use default ~/.codex

	cases := []struct {
		dir  string
		want SkillScope
	}{
		{filepath.Join(home, ".claude", "skills"), ScopeUser},
		{filepath.Join(home, ".codex", "skills"), ScopeUser},
		{filepath.Join(home, ".agents", "skills"), ScopeUser},
		{"~/.claude/skills", ScopeUser},
		{"/work/myrepo/.claude/skills", ScopeProject},
		{"/work/myrepo/.codex/skills", ScopeProject},
		{"/work/myrepo/.agents/skills", ScopeProject},
	}
	for _, c := range cases {
		if got := Scope(c.dir); got != c.want {
			t.Errorf("Scope(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

func TestScopeHonorsCodexHome(t *testing.T) {
	home := t.TempDir()
	codex := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codex)
	if got := Scope(filepath.Join(codex, "skills")); got != ScopeUser {
		t.Errorf("Scope under $CODEX_HOME/skills = %q, want user", got)
	}
}
