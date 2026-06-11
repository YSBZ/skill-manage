package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func mkSkill(t *testing.T, root, rel string) {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFindsTopLevelSkills(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "ce-plan")
	mkSkill(t, root, "ce-work")
	mkSkill(t, root, "deploy")
	// a non-skill dir
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 3 {
		t.Fatalf("want 3 skills, got %d: %+v", len(skills), skills)
	}
	// sorted by LinkName
	if skills[0].LinkName != "ce-plan" || skills[1].LinkName != "ce-work" || skills[2].LinkName != "deploy" {
		t.Errorf("unexpected skills: %+v", skills)
	}
}

func TestScanIgnoresNonSkillAndGit(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "real-skill")
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a .git subdir that happens to contain a SKILL.md must be ignored
	if err := os.WriteFile(filepath.Join(root, ".git", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].LinkName != "real-skill" {
		t.Errorf("want only real-skill, got %+v", skills)
	}
}

func TestScanDoesNotDescendIntoSkill(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "outer")
	// a nested skill inside outer must NOT be reported (direct-child rule)
	mkSkill(t, root, filepath.Join("outer", "inner"))

	skills, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].LinkName != "outer" {
		t.Errorf("nested skill should be skipped; got %+v", skills)
	}
}

func TestScanParsesDescription(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ce-plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: ce-plan\ndescription: Create structured implementation plans.\n---\n# body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Description != "Create structured implementation plans." {
		t.Errorf("description not parsed: %+v", skills)
	}
}

func TestScanNoFrontmatterEmptyDescription(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# no frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, _ := Scan(root)
	if len(skills) != 1 || skills[0].Description != "" {
		t.Errorf("missing frontmatter should yield empty description: %+v", skills)
	}
}

func TestScanEmptyRepo(t *testing.T) {
	skills, err := Scan(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 0 {
		t.Errorf("empty repo should yield no skills, got %+v", skills)
	}
}

func TestScanLinkNameSanitized(t *testing.T) {
	// macOS/APFS permits ':' at the POSIX layer, so we can verify the scanner
	// runs the name through pathutil end-to-end.
	root := t.TempDir()
	mkSkill(t, root, "ce:plan")
	skills, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("want 1 skill, got %+v", skills)
	}
	if skills[0].LogicalName != "ce:plan" {
		t.Errorf("logical name should be preserved, got %q", skills[0].LogicalName)
	}
	if skills[0].LinkName != "ce-plan" {
		t.Errorf("link name should be sanitized to ce-plan, got %q", skills[0].LinkName)
	}
}
