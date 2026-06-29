package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"skillmanage/internal/config"
	"skillmanage/internal/gitsync"
	"skillmanage/internal/reconcile"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func skipNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// bareRemoteWithSeed makes a bare repo (named, so distinct repos get distinct
// RepoName/dirs) with one commit on main.
func bareRemoteWithSeed(t *testing.T, name string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), name+".git")
	gitRun(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", bare)
	seed := t.TempDir()
	gitRun(t, seed, "clone", "-q", bare, ".")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "commit", "-q", "-m", "seed")
	gitRun(t, seed, "push", "-q", "origin", "main")
	return bare
}

// writeHandwritten creates a real skill dir with a SKILL.md under root.
func writeHandwritten(t *testing.T, root, name, desc string) {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// contribFixture wires a server with one source target holding a handwritten
// skill and one registered git repo cloned into the repos root. Returns the
// repo name, the source dir, and the bare remote path.
func contribFixture(t *testing.T, skill, desc string) (s *Server, repoName, srcDir, bare string) {
	t.Helper()
	skipNoGit(t)
	s = newTestServer(t)
	if s.syncer == nil {
		t.Skip("no syncer")
	}
	srcDir = t.TempDir()
	writeHandwritten(t, srcDir, skill, desc)

	bare = bareRemoteWithSeed(t, "fe-skills")
	repoName = reconcile.RepoName(bare)
	if err := os.MkdirAll(s.reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(s.reposRoot, repoName)
	gitRun(t, t.TempDir(), "clone", "-q", bare, mirror)
	gitRun(t, mirror, "config", "user.email", "t@example.com")
	gitRun(t, mirror, "config", "user.name", "t")

	s.cfg.Targets = []string{srcDir}
	s.cfg.Repos = []config.RepoConfig{{URL: bare}}
	return s, repoName, srcDir, bare
}

// TestContributeStages verifies a contribution STAGES the skill in the repo
// working tree + links it back + registers the enabled entry, WITHOUT pushing
// (push is deferred to the repo's 更新/上传). The remote stays unchanged.
func TestContributeStages(t *testing.T) {
	s, repo, srcDir, bare := contribFixture(t, "my-skill", "does a thing")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/contribute", s.token,
		map[string]string{"id": "my-skill", "root": srcDir, "repo": repo, "description": "does a thing"}))
	if w.Code != http.StatusOK {
		t.Fatalf("contribute: got %d body=%s", w.Code, w.Body.String())
	}

	// Source is now a symlink (relocated + linked back in place).
	lst, err := os.Lstat(filepath.Join(srcDir, "my-skill"))
	if err != nil || lst.Mode()&os.ModeSymlink == 0 {
		t.Errorf("source not a symlink after contribute: mode=%v err=%v", lst.Mode(), err)
	}
	// Skill landed (staged) in the repo working tree.
	if _, err := os.Stat(filepath.Join(s.reposRoot, repo, "my-skill", "SKILL.md")); err != nil {
		t.Errorf("skill missing in repo: %v", err)
	}
	// Enabled entry registered (防剪枝).
	wantSel := repo + "/my-skill"
	found := false
	for _, e := range s.cfg.Enabled {
		if e.Skill == wantSel {
			found = true
		}
	}
	if !found {
		t.Errorf("enabled entry %q not registered: %+v", wantSel, s.cfg.Enabled)
	}
	// NOT pushed: the remote does not carry the skill (staging only).
	if remoteHasSkill(t, bare, "my-skill") {
		t.Errorf("contribute should not push — remote unexpectedly has the skill")
	}
	// It shows as a pending change uploadable via 更新/上传.
	if k, has := s.syncer.Drift(s.detachedCtx(), filepath.Join(s.reposRoot, repo), "main").Has("my-skill"); !has || k != gitsync.DriftAdded {
		t.Errorf("staged skill not seen as added drift: kind=%q has=%v", k, has)
	}
}

func TestContributeEnabledSurvivesReconcile(t *testing.T) {
	s, repo, srcDir, _ := contribFixture(t, "keep-me", "x")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/contribute", s.token,
		map[string]string{"id": "keep-me", "root": srcDir, "repo": repo}))
	if w.Code != http.StatusOK {
		t.Fatalf("contribute: %d %s", w.Code, w.Body.String())
	}
	// A reconcile pass must NOT prune the in-place link (enabled entry exists).
	s.ReconcileOnly()
	lst, err := os.Lstat(filepath.Join(srcDir, "keep-me"))
	if err != nil || lst.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link pruned by reconcile: mode=%v err=%v", lst.Mode(), err)
	}
}

func TestQuickUploadRepushesCommittedUnpushed(t *testing.T) {
	s, repo, _, bare := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	// Simulate a contribution that committed but failed to push: a new skill,
	// committed, working tree clean, HEAD ahead — and the remote NOT advanced
	// (so a later push fast-forwards cleanly).
	writeHandwritten(t, mirror, "qu-skill", "quick")
	gitRun(t, mirror, "add", "-A")
	gitRun(t, mirror, "commit", "-q", "-m", "qu-skill quick")
	beforeCount := strings.TrimSpace(gitRun(t, mirror, "rev-list", "--count", "HEAD"))

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/quickupload", s.token,
		map[string]string{"repo": repo, "skill": "qu-skill"}))
	if w.Code != http.StatusOK {
		t.Fatalf("quickupload: %d %s", w.Code, w.Body.String())
	}
	// No new commit was created — the existing one was re-pushed.
	afterCount := strings.TrimSpace(gitRun(t, mirror, "rev-list", "--count", "HEAD"))
	if beforeCount != afterCount {
		t.Errorf("commit count changed %s→%s (should re-push, not re-commit)", beforeCount, afterCount)
	}
	// Remote now has the skill.
	verify := t.TempDir()
	gitRun(t, verify, "clone", "-q", bare, ".")
	if _, err := os.Stat(filepath.Join(verify, "qu-skill", "SKILL.md")); err != nil {
		t.Errorf("quick-uploaded skill missing on remote: %v", err)
	}
}

func TestQuickUploadCommitsWorkingTreeChange(t *testing.T) {
	s, repo, _, bare := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	// A skill already in the repo (committed + pushed), then edited locally.
	writeHandwritten(t, mirror, "edit-skill", "v1")
	gitRun(t, mirror, "add", "-A")
	gitRun(t, mirror, "commit", "-q", "-m", "edit-skill v1")
	gitRun(t, mirror, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(mirror, "edit-skill", "SKILL.md"), []byte("---\nname: edit-skill\ndescription: v2\n---\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/quickupload", s.token,
		map[string]string{"repo": repo, "skill": "edit-skill"}))
	if w.Code != http.StatusOK {
		t.Fatalf("quickupload modified: %d %s", w.Code, w.Body.String())
	}
	verify := t.TempDir()
	gitRun(t, verify, "clone", "-q", bare, ".")
	got, err := os.ReadFile(filepath.Join(verify, "edit-skill", "SKILL.md"))
	if err != nil || !strings.Contains(string(got), "edited") {
		t.Errorf("edit not pushed: %q err=%v", got, err)
	}
}

func TestContributeCSRFRejected(t *testing.T) {
	s, repo, srcDir, _ := contribFixture(t, "csrf-skill", "x")
	r := req("POST", "/api/contribute", s.token,
		map[string]string{"id": "csrf-skill", "root": srcDir, "repo": repo})
	r.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin contribute: got %d, want 403", w.Code)
	}
}

func TestContributeInvalidRepoRejected(t *testing.T) {
	s, _, srcDir, _ := contribFixture(t, "inv-skill", "x")
	for _, bad := range []string{"../escape", "a/b", ".."} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req("POST", "/api/contribute", s.token,
			map[string]string{"id": "inv-skill", "root": srcDir, "repo": bad}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("repo %q: got %d, want 400", bad, w.Code)
		}
	}
}

// cloneMirrorWithSkill clones bare into reposRoot/<name>, sets a commit
// identity, and (when skill != "") commits+pushes one skill so the repo upstream
// carries it. Returns the repo name and mirror dir.
func cloneMirrorWithSkill(t *testing.T, reposRoot, bare, skill string) (string, string) {
	t.Helper()
	name := reconcile.RepoName(bare)
	mirror := filepath.Join(reposRoot, name)
	gitRun(t, t.TempDir(), "clone", "-q", bare, mirror)
	gitRun(t, mirror, "config", "user.email", "t@example.com")
	gitRun(t, mirror, "config", "user.name", "t")
	if skill != "" {
		writeHandwritten(t, mirror, skill, "from "+name)
		gitRun(t, mirror, "add", "-A")
		gitRun(t, mirror, "commit", "-q", "-m", "seed "+skill)
		gitRun(t, mirror, "push", "-q", "origin", "main")
	}
	return name, mirror
}

func remoteHasSkill(t *testing.T, bare, skill string) bool {
	t.Helper()
	v := t.TempDir()
	gitRun(t, v, "clone", "-q", bare, ".")
	_, err := os.Stat(filepath.Join(v, skill, "SKILL.md"))
	return err == nil
}

func TestMoveGitToGit(t *testing.T) {
	skipNoGit(t)
	s := newTestServer(t)
	if s.syncer == nil {
		t.Skip("no syncer")
	}
	if err := os.MkdirAll(s.reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	bareA, bareB := bareRemoteWithSeed(t, "repo-a"), bareRemoteWithSeed(t, "repo-b")
	nameA, _ := cloneMirrorWithSkill(t, s.reposRoot, bareA, "mover")
	nameB, _ := cloneMirrorWithSkill(t, s.reposRoot, bareB, "")
	srcDir := t.TempDir()
	s.cfg.Repos = []config.RepoConfig{{URL: bareA}, {URL: bareB}}
	s.cfg.Enabled = []config.EnabledEntry{{Skill: nameA + "/mover", Target: srcDir, Mode: config.ModeSnapshot}}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move", s.token,
		map[string]string{"name": "mover", "fromRepo": nameA, "toRepo": nameB}))
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	// Staged: present in B's working tree, gone from A's working tree.
	if _, err := os.Stat(filepath.Join(s.reposRoot, nameB, "mover", "SKILL.md")); err != nil {
		t.Errorf("mover missing in dest mirror: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.reposRoot, nameA, "mover")); !os.IsNotExist(err) {
		t.Errorf("mover not removed from source mirror")
	}
	// Not pushed: dest upstream does NOT yet have it; source upstream STILL has it
	// (the removal is a pending deletion to upload via 更新). 移动只暂存，不自动推送。
	if remoteHasSkill(t, bareB, "mover") {
		t.Errorf("dest upstream should not have mover yet (move stages, no push)")
	}
	if !remoteHasSkill(t, bareA, "mover") {
		t.Errorf("source upstream unexpectedly lost mover (move should not push the removal)")
	}
	// Enabled entry migrated A→B.
	for _, e := range s.cfg.Enabled {
		if e.Skill == nameA+"/mover" {
			t.Errorf("old enabled selector still present: %+v", e)
		}
	}
	found := false
	for _, e := range s.cfg.Enabled {
		if e.Skill == nameB+"/mover" {
			found = true
		}
	}
	if !found {
		t.Errorf("migrated selector %s/mover missing: %+v", nameB, s.cfg.Enabled)
	}
	// Ledger records the new home.
	cm, _ := config.LoadContribManifest(s.centralDir)
	if cm.Skills["mover"].Location != nameB {
		t.Errorf("ledger location = %q, want %s", cm.Skills["mover"].Location, nameB)
	}
}

func TestMoveGitToLocal(t *testing.T) {
	skipNoGit(t)
	s := newTestServer(t)
	if s.syncer == nil {
		t.Skip("no syncer")
	}
	if err := os.MkdirAll(s.reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	bareA := bareRemoteWithSeed(t, "repo-a")
	nameA, _ := cloneMirrorWithSkill(t, s.reposRoot, bareA, "backhome")
	s.cfg.Repos = []config.RepoConfig{{URL: bareA}}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move", s.token,
		map[string]string{"name": "backhome", "fromRepo": nameA, "toRepo": "local"}))
	if w.Code != http.StatusOK {
		t.Fatalf("move to local: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.personalStore, "backhome", "SKILL.md")); err != nil {
		t.Errorf("skill not in @local store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.reposRoot, nameA, "backhome")); !os.IsNotExist(err) {
		t.Errorf("skill not removed from source mirror")
	}
	// The store copy is the durable new home; the source repo's removal is a
	// pending deletion (not pushed), so the source upstream still carries it.
	if !remoteHasSkill(t, bareA, "backhome") {
		t.Errorf("source upstream unexpectedly lost the skill (move should not push the removal)")
	}
	cm, _ := config.LoadContribManifest(s.centralDir)
	if cm.Skills["backhome"].Location != "local" {
		t.Errorf("ledger location = %q, want local", cm.Skills["backhome"].Location)
	}
}

func TestMoveRejectsSameRepoAndCSRF(t *testing.T) {
	skipNoGit(t)
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/move", s.token,
		map[string]string{"name": "x", "fromRepo": "a", "toRepo": "a"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("same repo: got %d, want 400", w.Code)
	}
	r := req("POST", "/api/move", s.token, map[string]string{"name": "x", "fromRepo": "a", "toRepo": "b"})
	r.Header.Set("Origin", "http://evil.example.com")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: got %d, want 403", w.Code)
	}
}

func TestSanitizeCommitDesc(t *testing.T) {
	if got := commitMessage("foo", "line one\nline two\twith\ttabs"); got != "foo line one line two with tabs" {
		t.Errorf("multiline desc folded wrong: %q", got)
	}
	if got := commitMessage("foo", "   "); got != "foo" {
		t.Errorf("empty desc fallback wrong: %q", got)
	}
	long := strings.Repeat("x", 300)
	got := commitMessage("foo", long)
	if len([]rune(got)) > len("foo ")+maxCommitDescRunes {
		t.Errorf("desc not truncated: len=%d", len([]rune(got)))
	}
}

// TestUploadMultipleSkills stages + commits + pushes two working-tree-new skills
// in one shot via /api/upload, and confirms both land on the remote.
func TestUploadMultipleSkills(t *testing.T) {
	s, repo, _, bare := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	writeHandwritten(t, mirror, "up-a", "alpha")
	writeHandwritten(t, mirror, "up-b", "beta")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/upload", s.token,
		map[string]any{"repo": repo, "skills": []string{"up-a", "up-b"}, "message": "两个 skill"}))
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if !remoteHasSkill(t, bare, "up-a") || !remoteHasSkill(t, bare, "up-b") {
		t.Errorf("uploaded skills missing on remote")
	}
}

// TestUploadNothingToPush reports nothing:true (not an error) when the selected
// skill has no local changes.
func TestUploadNothingToPush(t *testing.T) {
	s, repo, _, _ := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	writeHandwritten(t, mirror, "clean", "v1")
	gitRun(t, mirror, "add", "-A")
	gitRun(t, mirror, "commit", "-q", "-m", "clean v1")
	gitRun(t, mirror, "push", "-q", "origin", "main")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/upload", s.token,
		map[string]any{"repo": repo, "skills": []string{"clean"}, "message": "noop"}))
	if w.Code != http.StatusOK {
		t.Fatalf("upload clean: %d %s", w.Code, w.Body.String())
	}
	if d := decodeBody(t, w); d["nothing"] != true {
		t.Errorf("clean skill upload: want nothing:true, got %v", d)
	}
}

// TestUploadRejectsCSRF guards the push endpoint against cross-origin POST.
func TestUploadRejectsCSRF(t *testing.T) {
	s, repo, _, _ := contribFixture(t, "ignored", "x")
	r := req("POST", "/api/upload", s.token, map[string]any{"repo": repo, "skills": []string{"x"}})
	r.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin upload: got %d, want 403", w.Code)
	}
}

// TestRepoDriftListsChangedSkills surfaces a repo's unpushed skills for the
// upload dialog, excluding clean ones.
func TestRepoDriftListsChangedSkills(t *testing.T) {
	s, repo, _, _ := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	writeHandwritten(t, mirror, "drifted", "new")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("GET", "/api/repo-drift?repo="+repo, s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("repo-drift: %d %s", w.Code, w.Body.String())
	}
	d := decodeBody(t, w)
	entries, _ := d["entries"].([]any)
	found := false
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok && m["skill"] == "drifted" {
			found = true
		}
	}
	if !found {
		t.Errorf("repo-drift missing 'drifted': %v", d)
	}
}

// seedNestedSkill commits skills/<name>/SKILL.md in the mirror and pushes it, so
// the repo's existing layout nests skills under a "skills/" directory.
func seedNestedSkill(t *testing.T, mirror, name string) {
	t.Helper()
	dir := filepath.Join(mirror, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: seed\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, mirror, "add", "-A")
	gitRun(t, mirror, "commit", "-q", "-m", "seed nested "+name)
	gitRun(t, mirror, "push", "-q", "origin", "main")
}

// TestContributeLandsInRepoSkillRoot verifies a contribution into a repo that
// nests skills under skills/ lands alongside them, not at the repo root.
func TestContributeLandsInRepoSkillRoot(t *testing.T) {
	s, repo, srcDir, _ := contribFixture(t, "newpage", "a page skill")
	mirror := filepath.Join(s.reposRoot, repo)
	seedNestedSkill(t, mirror, "existing")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/contribute", s.token,
		map[string]string{"id": "newpage", "root": srcDir, "repo": repo, "description": "a page skill"}))
	if w.Code != http.StatusOK {
		t.Fatalf("contribute: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(mirror, "skills", "newpage", "SKILL.md")); err != nil {
		t.Errorf("contributed skill not under skills/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "newpage", "SKILL.md")); err == nil {
		t.Errorf("contributed skill wrongly placed at repo root")
	}
}

// TestDeleteRepoSkillLeavesPendingDeletion removes a nested skill from the repo
// working tree and confirms it becomes a pending deletion (not yet pushed).
func TestDeleteRepoSkillLeavesPendingDeletion(t *testing.T) {
	s, repo, _, _ := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	seedNestedSkill(t, mirror, "keep") // sibling so the repo's skill-root stays "skills"
	seedNestedSkill(t, mirror, "victim")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("DELETE", "/api/repo-skill", s.token,
		map[string]string{"repo": repo, "skill": "victim"}))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(mirror, "skills", "victim")); !os.IsNotExist(err) {
		t.Errorf("victim dir still present after delete: err=%v", err)
	}
	if k, has := s.syncer.Drift(s.detachedCtx(), mirror, "main").Has("victim"); !has || k != gitsync.DriftDeleted {
		t.Errorf("expected pending deletion drift for victim, got kind=%q has=%v", k, has)
	}
}

// TestUploadPushesDeletion confirms a locally-removed (pending-deletion) skill is
// propagated to the remote when uploaded by its repo-relative path.
func TestUploadPushesDeletion(t *testing.T) {
	s, repo, _, bare := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	seedNestedSkill(t, mirror, "keep") // sibling so the repo's skill-root stays "skills"
	seedNestedSkill(t, mirror, "gone")
	if err := os.RemoveAll(filepath.Join(mirror, "skills", "gone")); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("POST", "/api/upload", s.token,
		map[string]any{"repo": repo, "skills": []string{"skills/gone"}, "message": "删除 gone"}))
	if w.Code != http.StatusOK {
		t.Fatalf("upload deletion: %d %s", w.Code, w.Body.String())
	}
	if remoteHasSkill(t, bare, filepath.Join("skills", "gone")) {
		t.Errorf("deletion not pushed: skills/gone still on remote")
	}
}

// TestSkillAuthorship derives a git skill's creator from git log (the test commit
// author is "t").
func TestSkillAuthorship(t *testing.T) {
	s, repo, _, _ := contribFixture(t, "ignored", "x")
	mirror := filepath.Join(s.reposRoot, repo)
	seedNestedSkill(t, mirror, "authored")

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req("GET", "/api/skill-authorship?repo="+repo+"&skill=authored", s.token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("authorship: %d %s", w.Code, w.Body.String())
	}
	d := decodeBody(t, w)
	a, _ := d["authorship"].(map[string]any)
	if a == nil || a["creator"] != "t" {
		t.Errorf("creator = %v, want t (body=%s)", a, w.Body.String())
	}
}
