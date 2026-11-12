package releasev2

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepoReleaseProfileParses keeps the checked-in smartpipe.yaml release
// section valid: plans, executor commands and artifact slots depend on it.
func TestRepoReleaseProfileParses(t *testing.T) {
	path := filepath.Join("..", "..", "..", "smartpipe.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("repo profile not available: %v", err)
	}
	profile, err := ParseProfile(raw)
	if err != nil {
		t.Fatalf("smartpipe.yaml release section: %v", err)
	}
	targets, err := profile.PlanTargets(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	for _, target := range targets {
		if len(target.Artifacts) == 0 || target.Artifacts[0].Role != "server-binary" {
			t.Fatalf("target %s artifact rules = %+v", target.ID, target.Artifacts)
		}
	}
	if cfg := profile.Release.Publishing["update_service"]; cfg["product"] != "smartpipe" {
		t.Fatalf("publishing config = %+v", cfg)
	}
}
