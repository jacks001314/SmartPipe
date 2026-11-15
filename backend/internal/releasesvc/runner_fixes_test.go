package releasesvc

import (
	"context"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

type leaseFake struct {
	fakeStore
	requeued int
}

func (f *leaseFake) RequeueExpiredLeases(context.Context) (int, error) { return f.requeued, nil }

func TestRunnerRequeuesExpiredLeasesEachTick(t *testing.T) {
	f := &leaseFake{fakeStore: fakeStore{runs: []releasev2.Run{{ID: "run_1", Status: releasev2.RunVerifying}}}, requeued: 2}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.requeued != 2 {
		t.Fatalf("requeued = %d", f.requeued)
	}
}

func TestRunnerMovesPublishRunToArtifactsReady(t *testing.T) {
	f := &fakeStore{
		runs: []releasev2.Run{{
			ID: "run_1", Status: releasev2.RunVerifying, Mode: releasev2.ModePublish,
			ManifestStorageKey: "manifests/run_1/run.json",
		}},
		targets: []releasev2.Target{{TargetID: "linux-amd64", Required: true, Status: releasev2.TargetReady}},
	}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunArtifactsReady {
		t.Fatalf("status = %s, want artifacts_ready", f.runs[0].Status)
	}
}

func TestRunnerClosesNeedsAttentionRunWhenTargetsAreReady(t *testing.T) {
	f := &fakeStore{
		runs: []releasev2.Run{{
			ID: "run_1", Status: releasev2.RunNeedsAttention, Mode: releasev2.ModePublish,
			ManifestStorageKey: "manifests/run_1/run.json",
		}},
		targets: []releasev2.Target{{TargetID: "windows-amd64", Required: true, Status: releasev2.TargetReady}},
	}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunArtifactsReady {
		t.Fatalf("status = %s, want artifacts_ready", f.runs[0].Status)
	}
}

func TestRunnerKeepsNeedsAttentionRunWithFailedTarget(t *testing.T) {
	f := &fakeStore{
		runs: []releasev2.Run{{
			ID: "run_1", Status: releasev2.RunNeedsAttention, Mode: releasev2.ModePublish,
			ManifestStorageKey: "manifests/run_1/run.json",
		}},
		targets: []releasev2.Target{
			{TargetID: "windows-amd64", Required: true, Status: releasev2.TargetFailed},
			{TargetID: "darwin-arm64", Required: true, Status: releasev2.TargetReady},
		},
	}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunNeedsAttention {
		t.Fatalf("status = %s, want needs_attention", f.runs[0].Status)
	}
}

func TestRunnerWaitsForRunManifestBeforeArtifactsReady(t *testing.T) {
	f := &fakeStore{
		runs:    []releasev2.Run{{ID: "run_1", Status: releasev2.RunBuilding, Mode: releasev2.ModePublish}},
		targets: []releasev2.Target{{TargetID: "linux-amd64", Required: true, Status: releasev2.TargetReady}},
	}
	r := &Runner{Store: f}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunBuilding {
		t.Fatalf("status = %s, want building while manifest is missing", f.runs[0].Status)
	}
	f.runs[0].ManifestStorageKey = "manifests/run_1/run.json"
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunArtifactsReady {
		t.Fatalf("status = %s, want artifacts_ready", f.runs[0].Status)
	}
}

func TestRunnerLegacyVerifyingRunWithFailedTargetGoesToAttention(t *testing.T) {
	f := &fakeStore{
		runs: []releasev2.Run{{
			ID: "run_1", Status: releasev2.RunVerifying, Mode: releasev2.ModeBuildOnly,
			ManifestStorageKey: "manifests/run_1/run.json",
		}},
		targets: []releasev2.Target{
			{TargetID: "windows-amd64", Required: true, Status: releasev2.TargetFailed, ErrorMessage: "node: command not found"},
		},
	}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunNeedsAttention {
		t.Fatalf("status = %s, want needs_attention", f.runs[0].Status)
	}
}

func TestRunnerDoesNotFinishWhileTargetUploadedOnly(t *testing.T) {
	f := &fakeStore{
		runs:    []releasev2.Run{{ID: "run_1", Status: releasev2.RunBuilding, Mode: releasev2.ModeBuildOnly}},
		targets: []releasev2.Target{{TargetID: "linux-amd64", Status: releasev2.TargetUploaded}},
	}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunBuilding {
		t.Fatalf("status = %s, want building", f.runs[0].Status)
	}
}
