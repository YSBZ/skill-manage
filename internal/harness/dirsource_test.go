package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverDefaultDirectorySources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Absent → empty (the user registers it manually if elsewhere, R2.3).
	if got := DiscoverDefaultDirectorySources(); len(got) != 0 {
		t.Fatalf("no ~/.agents/skills present, want empty, got %+v", got)
	}

	// Present → surfaced as a "~"-relative directory source, NOT a target.
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := DiscoverDefaultDirectorySources()
	if len(got) != 1 || got[0] != "~/.agents/skills/" {
		t.Fatalf("want [~/.agents/skills/], got %+v", got)
	}
	// It must not leak into the link-target discovery.
	for _, target := range DiscoverDefaultTargets() {
		if target == "~/.agents/skills/" {
			t.Errorf("~/.agents/skills must be a directory source, not a link target")
		}
	}
}
