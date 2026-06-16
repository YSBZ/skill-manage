package scanner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestScanSkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "real-skill")
	mkSkill(t, root, filepath.Join("node_modules", "pkg", "buried-skill"))
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LinkName != "real-skill" {
		t.Fatalf("node_modules skill must be skipped, got %+v", got)
	}
}

func TestScanDepthCapStopsDescent(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "shallow")
	deep := strings.Repeat("d"+string(filepath.Separator), maxScanDepth+2)
	mkSkill(t, root, filepath.Join(deep, "too-deep"))
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range got {
		if s.LinkName == "too-deep" {
			t.Fatalf("skill past depth cap should be skipped, got %+v", got)
		}
		if s.LinkName == "shallow" {
			found = true
		}
	}
	if !found {
		t.Fatalf("shallow skill within cap should be found, got %+v", got)
	}
}

func TestScanDirBudgetDoesNotHang(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxScanDirs+200; i++ {
		if err := os.MkdirAll(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Scan(root); err != nil {
		t.Fatalf("Scan over budget must not error, got %v", err)
	}
}
