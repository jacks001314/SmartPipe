package releasemcp

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestManifestBlockersIgnoresRunLevelArtifacts(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	run := &releasev2.Run{ID: "r1"}
	targets := []releasev2.Target{
		{TargetID: "windows-amd64", OS: "windows", Arch: "amd64", Required: true, Status: releasev2.TargetReady},
	}
	artifacts := []releasev2.Artifact{
		{ID: 1, RunID: "r1", TargetID: "windows-amd64", Attempt: 1, Role: "installer", Name: "setup.exe", Size: 10, SHA256: digest},
		{ID: 2, RunID: "r1", TargetID: "", Attempt: 1, Role: "upgrade_bundle", Name: "upgrade-bundle.json", Size: 5, SHA256: digest},
	}
	manifest := &releasev2.RunArtifactManifest{
		Schema: releasev2.ArtifactManifestSchema,
		RunID:  "r1",
		Targets: []releasev2.ArtifactManifest{{
			Schema:   releasev2.ArtifactManifestSchema,
			RunID:    "r1",
			TargetID: "windows-amd64",
			OS:       "windows",
			Arch:     "amd64",
			Artifacts: []releasev2.ManifestArtifact{
				{Role: "installer", Name: "setup.exe", Path: "dist/setup.exe", Size: 10, SHA256: digest},
			},
		}},
	}
	if blockers := manifestBlockers(run, targets, artifacts, manifest); len(blockers) != 0 {
		t.Fatalf("unexpected blockers: %v", blockers)
	}
}
