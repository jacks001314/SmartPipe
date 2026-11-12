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

func TestRunnerLeavesPublishRunVerifyingForAgent(t *testing.T) {
	f := &fakeStore{
		runs:    []releasev2.Run{{ID: "run_1", Status: releasev2.RunVerifying, Mode: releasev2.ModePublish}},
		targets: []releasev2.Target{{TargetID: "linux-amd64", Required: true, Status: releasev2.TargetReady}},
	}
	if err := (&Runner{Store: f}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunVerifying {
		t.Fatalf("status = %s, want verifying (agent stages it)", f.runs[0].Status)
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
