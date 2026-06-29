package config

import (
	"path/filepath"
	"testing"
)

func TestContribManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Missing file → empty ledger, no error.
	m, err := LoadContribManifest(dir)
	if err != nil || len(m.Skills) != 0 {
		t.Fatalf("empty load: skills=%d err=%v", len(m.Skills), err)
	}

	if err := UpsertContrib(dir, "ce-plan", "make a plan", "fe-skills"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertContrib(dir, "ce-work", "do the work", "local"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadContribManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e := got.Skills["ce-plan"]; e.Description != "make a plan" || e.Location != "fe-skills" {
		t.Errorf("ce-plan = %+v", e)
	}
	if e := got.Skills["ce-work"]; e.Location != "local" {
		t.Errorf("ce-work = %+v", e)
	}

	// Re-upsert moves location; blank description preserves the prior one.
	if err := UpsertContrib(dir, "ce-plan", "", "other-repo"); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadContribManifest(dir)
	if e := got.Skills["ce-plan"]; e.Description != "make a plan" || e.Location != "other-repo" {
		t.Errorf("after move ce-plan = %+v, want desc preserved + location other-repo", e)
	}

	if ContribManifestPath(dir) != filepath.Join(dir, "contrib-manifest.yaml") {
		t.Errorf("path = %s", ContribManifestPath(dir))
	}
}
