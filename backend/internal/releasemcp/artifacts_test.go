package releasemcp

import (
	"strings"
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

func TestManifestBlockersPreferRunManifest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	run := &releasev2.Run{ID: "run_1"}
	targets := []releasev2.Target{
		{TargetID: "windows-amd64", Required: true, Artifacts: []releasev2.ArtifactRule{{Role: "installer"}}},
	}
	artifacts := []releasev2.Artifact{
		{TargetID: "windows-amd64", Role: "installer", Name: "SyntropySetup.exe", Size: 10, SHA256: digest},
	}
	manifest := &releasev2.RunArtifactManifest{
		Schema: releasev2.ArtifactManifestSchema, RunID: run.ID,
		Targets: []releasev2.ArtifactManifest{{
			Schema: releasev2.ArtifactManifestSchema, RunID: run.ID, TargetID: "windows-amd64", OS: "windows", Arch: "amd64",
			Artifacts: []releasev2.ManifestArtifact{{
				Role: "installer", Name: "SyntropySetup.exe", Path: "dist/SyntropySetup.exe", Size: 10, SHA256: digest, Required: true,
			}},
		}},
	}
	if blockers := manifestBlockers(run, targets, artifacts, manifest); len(blockers) != 0 {
		t.Fatalf("unexpected blockers: %v", blockers)
	}

	missing := append([]releasev2.Artifact(nil), artifacts...)
	missing[0].SHA256 = "sha256:" + strings.Repeat("b", 64)
	if blockers := manifestBlockers(run, targets, missing, manifest); len(blockers) != 1 {
		t.Fatalf("digest mismatch blockers = %v", blockers)
	}

	if blockers := manifestBlockers(run, targets, nil, manifest); len(blockers) != 1 {
		t.Fatalf("missing row blockers = %v", blockers)
	}

	extra := append(append([]releasev2.Artifact(nil), artifacts...), releasev2.Artifact{
		TargetID: "windows-amd64", Role: "blockmap", Name: "SyntropySetup.exe.blockmap", Size: 1, SHA256: digest,
	})
	if blockers := manifestBlockers(run, targets, extra, manifest); len(blockers) != 1 {
		t.Fatalf("extra row blockers = %v", blockers)
	}

	empty := &releasev2.RunArtifactManifest{Schema: releasev2.ArtifactManifestSchema, RunID: run.ID}
	blockers := manifestBlockers(run, targets, artifacts, empty)
	if len(blockers) != 3 {
		t.Fatalf("empty manifest blockers = %v", blockers)
	}
}
