package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/source"
)

func mkdirT(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func symlinkT(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.Symlink(src, dst); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryClassifiesSources(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	t.Setenv("HOME", base) // hermetic: DiscoverDefaultDirectorySources looks under base

	target := mkdirT(t, filepath.Join(base, "proj", ".claude", "skills"))
	agents := filepath.Join(base, ".agents")
	agentsSkills := mkdirT(t, filepath.Join(agents, "skills"))

	// git (manifest-owned symlink under reposRoot)
	gsrc := mkdirT(t, filepath.Join(s.reposRoot, "myrepo", "ce-plan"))
	os.WriteFile(filepath.Join(gsrc, "SKILL.md"), []byte("---\nname: ce-plan\ndescription: plan\n---\n"), 0o644)
	symlinkT(t, gsrc, filepath.Join(target, "ce-plan"))

	// local (manifest-owned symlink under personalStore)
	lsrc := mkdirT(t, filepath.Join(s.personalStore, "my-local"))
	os.WriteFile(filepath.Join(lsrc, "SKILL.md"), []byte("---\nname: my-local\n---\n"), 0o644)
	symlinkT(t, lsrc, filepath.Join(target, "my-local"))

	// skills.sh (symlink into .agents/skills + lockfile)
	ssrc := mkdirT(t, filepath.Join(agentsSkills, "find-skills"))
	os.WriteFile(filepath.Join(ssrc, "SKILL.md"), []byte("---\nname: find-skills\n---\n"), 0o644)
	symlinkT(t, ssrc, filepath.Join(target, "find-skills"))

	// skills.sh with a malicious sourceUrl that must be stripped
	esrc := mkdirT(t, filepath.Join(agentsSkills, "evil"))
	os.WriteFile(filepath.Join(esrc, "SKILL.md"), []byte("---\nname: evil\n---\n"), 0o644)
	symlinkT(t, esrc, filepath.Join(target, "evil"))

	// handwritten (real dir, no manifest record)
	hand := mkdirT(t, filepath.Join(target, "hand-made"))
	os.WriteFile(filepath.Join(hand, "SKILL.md"), []byte("---\nname: hand-made\n---\n"), 0o644)

	lock := `{"version":3,"skills":{
	  "find-skills":{"source":"vercel-labs/skills","sourceUrl":"https://github.com/vercel-labs/skills.git","skillFolderHash":"a"},
	  "evil":{"source":"x","sourceUrl":"javascript:alert(1)","skillFolderHash":"b"}
	}}`
	os.WriteFile(filepath.Join(agents, ".skill-lock.json"), []byte(lock), 0o644)

	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.cfg.DirectorySources = []config.DirectorySource{{Path: agentsSkills}}
	s.manifest.Links = []config.LinkRecord{
		{Name: "ce-plan", Target: target, Source: gsrc, LinkType: config.LinkSymlink},
		{Name: "my-local", Target: target, Source: lsrc, LinkType: config.LinkSymlink},
	}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleInventory(w, req("GET", "/api/inventory?target="+url.QueryEscape(target), s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("inventory: got %d, body=%s", w.Code, w.Body.String())
	}
	var resp inventoryResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byName := map[string]inventoryItem{}
	for _, it := range resp.Items {
		byName[it.Name] = it
	}
	if got := byName["ce-plan"]; got.Kind != source.KindGit || got.Repo != "myrepo" || !got.Managed || !got.Enabled || got.Selector != "myrepo/ce-plan" {
		t.Errorf("ce-plan: %+v", got)
	}
	if got := byName["my-local"]; got.Kind != source.KindLocal || !got.Managed || got.Selector != "@local/my-local" {
		t.Errorf("my-local: %+v", got)
	}
	if got := byName["find-skills"]; got.Kind != source.KindSkillsSh || got.SourceURL != "https://github.com/vercel-labs/skills.git" || got.Managed {
		t.Errorf("find-skills: %+v", got)
	}
	if got := byName["evil"]; got.Kind != source.KindSkillsSh || got.SourceURL != "" {
		t.Errorf("evil sourceUrl must be stripped: %+v", got)
	}
	if got := byName["hand-made"]; got.Kind != source.KindHandwritten || got.Managed {
		t.Errorf("hand-made: %+v", got)
	}
	if resp.Scope != "project" {
		t.Errorf("scope = %q, want project", resp.Scope)
	}
}

func TestInventoryUnknownTargetRejected(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.handleInventory(w, req("GET", "/api/inventory?target=/not/configured", s.token, nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown target: got %d, want 400", w.Code)
	}
}

func TestInventoryEmptyTargetDir(t *testing.T) {
	s := newTestServer(t)
	target := mkdirT(t, filepath.Join(t.TempDir(), "skills"))
	s.mu.Lock()
	s.cfg.Targets = []string{target}
	s.mu.Unlock()
	w := httptest.NewRecorder()
	s.handleInventory(w, req("GET", "/api/inventory?target="+url.QueryEscape(target), s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var resp inventoryResp
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Items == nil {
		t.Error("empty dir must yield non-nil items slice")
	}
	if len(resp.Items) != 0 {
		t.Errorf("empty dir items = %+v", resp.Items)
	}
}
