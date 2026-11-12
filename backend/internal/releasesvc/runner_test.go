package releasesvc

import (
	"context"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

type fakeStore struct {
	runs      []releasev2.Run
	targets   []releasev2.Target
	executors []releasev2.Executor
	events    []releasev2.Event
}

func (f *fakeStore) ListActiveReleaseV2Runs(context.Context, int) ([]releasev2.Run, error) {
	return append([]releasev2.Run(nil), f.runs...), nil
}
func (f *fakeStore) ListTargets(context.Context, string) ([]releasev2.Target, error) {
	return append([]releasev2.Target(nil), f.targets...), nil
}
func (f *fakeStore) ListExecutors(context.Context) ([]releasev2.Executor, error) {
	return append([]releasev2.Executor(nil), f.executors...), nil
}
func (f *fakeStore) UpdateRunStatus(_ context.Context, id string, from, to releasev2.RunStatus, code, message string) (bool, error) {
	for i := range f.runs {
		if f.runs[i].ID != id || f.runs[i].Status != from {
			continue
		}
		f.runs[i].Status = to
		return true, nil
	}
	return false, nil
}
func (f *fakeStore) RequeueExpiredLeases(context.Context) (int, error) { return 0, nil }
func (f *fakeStore) AppendEvent(_ context.Context, e releasev2.Event) error {
	f.events = append(f.events, e)
	return nil
}

func TestRunnerAdvancesBuildOnlyRun(t *testing.T) {
	f := &fakeStore{
		runs:      []releasev2.Run{{ID: "r1", Status: releasev2.RunQueued, Mode: releasev2.ModeBuildOnly}},
		targets:   []releasev2.Target{{RunID: "r1", TargetID: "windows-amd64", ExecutorID: "win-1", Required: true, Status: releasev2.TargetQueued}},
		executors: []releasev2.Executor{{ID: "win-1", Status: "online"}},
	}
	r := &Runner{Store: f}
	ctx := context.Background()
	for i, want := range []releasev2.RunStatus{releasev2.RunPreparing, releasev2.RunBuilding, releasev2.RunBuilding} {
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if f.runs[0].Status != want {
			t.Fatalf("tick %d status=%s want %s", i, f.runs[0].Status, want)
		}
	}
	f.targets[0].Status = releasev2.TargetReady
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunVerifying {
		t.Fatalf("status=%s want verifying", f.runs[0].Status)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunBuilt {
		t.Fatalf("status=%s want built", f.runs[0].Status)
	}
}

func TestRunnerBlocksOfflineExecutor(t *testing.T) {
	f := &fakeStore{
		runs:      []releasev2.Run{{ID: "r1", Status: releasev2.RunPreparing, Mode: releasev2.ModeBuildOnly}},
		targets:   []releasev2.Target{{RunID: "r1", TargetID: "darwin-arm64", ExecutorID: "mac-1", Required: true, Status: releasev2.TargetQueued}},
		executors: []releasev2.Executor{{ID: "mac-1", Status: "offline"}},
	}
	r := &Runner{Store: f}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunBlocked {
		t.Fatalf("status=%s want blocked", f.runs[0].Status)
	}
}

func TestRunnerRespectsMaxActive(t *testing.T) {
	f := &fakeStore{
		runs: []releasev2.Run{
			{ID: "r1", Status: releasev2.RunQueued, Mode: releasev2.ModeBuildOnly},
			{ID: "r2", Status: releasev2.RunQueued, Mode: releasev2.ModeBuildOnly},
		},
		targets:   []releasev2.Target{{RunID: "r1", TargetID: "linux-amd64", ExecutorID: "e1", Required: true, Status: releasev2.TargetQueued}},
		executors: []releasev2.Executor{{ID: "e1", Status: "online"}},
	}
	r := &Runner{Store: f, MaxActive: 1}
	ctx := context.Background()

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunPreparing {
		t.Fatalf("r1 status=%s want preparing", f.runs[0].Status)
	}
	if f.runs[1].Status != releasev2.RunQueued {
		t.Fatalf("r2 status=%s want queued while slot is taken", f.runs[1].Status)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runs[0].Status != releasev2.RunBuilding || f.runs[1].Status != releasev2.RunQueued {
		t.Fatalf("r1=%s r2=%s want building/queued", f.runs[0].Status, f.runs[1].Status)
	}
	waits := 0
	for _, e := range f.events {
		if e.Kind == "run.waiting" {
			waits++
		}
	}
	if waits != 1 {
		t.Fatalf("run.waiting events = %d, want exactly 1 for the held run", waits)
	}

	// 第一个运行收尾后，排队的运行应被放行。
	f.runs[0].Status = releasev2.RunBuilt
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runs[1].Status != releasev2.RunPreparing {
		t.Fatalf("r2 status=%s want preparing after slot freed", f.runs[1].Status)
	}
}