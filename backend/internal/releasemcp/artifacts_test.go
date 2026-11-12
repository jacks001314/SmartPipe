package releasemcp

import (
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestArtifactGapsDetectMissingRoles(t *testing.T) {
	targets := []releasev2.Target{
		{TargetID: "windows-amd64", Required: true, Artifacts: []releasev2.ArtifactRule{{Role: "installer"}, {Role: "blockmap"}}},
		{TargetID: "darwin-arm64", Required: true, Artifacts: []releasev2.ArtifactRule{{Role: "installer"}}},
	}
	artifacts := []releasev2.Artifact{
		{TargetID: "windows-amd64", Role: "installer"},
	}
	gaps := artifactGaps(targets, artifacts)
	if len(gaps) != 2 {
		t.Fatalf("gaps = %v", gaps)
	}
	artifacts = append(artifacts,
		releasev2.Artifact{TargetID: "windows-amd64", Role: "blockmap"},
		releasev2.Artifact{TargetID: "darwin-arm64", Role: "installer"},
	)
	if gaps := artifactGaps(targets, artifacts); len(gaps) != 0 {
		t.Fatalf("unexpected gaps = %v", gaps)
	}
}
