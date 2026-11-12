package releasev2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRepo struct {
	runs        map[string]*Run
	targets     map[string][]Target
	events      []Event
	artifacts   map[string][]Artifact
	publication []Publication
	nextEvent   int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{runs: map[string]*Run{}, targets: map[string][]Target{}, artifacts: map[string][]Artifact{}}
}

func (f *fakeRepo) seed(run Run, targets ...Target) {
	copyRun := run
	if copyRun.CreatedAt.IsZero() {
		copyRun.CreatedAt = time.Now().UTC()
	}
	f.runs[run.ID] = &copyRun
	for i := range targets {
		targets[i].RunID = run.ID
	}
	f.targets[run.ID] = targets
}

func (f *fakeRepo) CreateReleaseV2Plan(context.Context, *Plan) error { return nil }
func (f *fakeRepo) GetReleaseV2Plan(context.Context, string) (*Plan, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRepo) CreateRunFromPlan(context.Context, *Run, []Target) (*Run, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRepo) GetRun(_ context.Context, id string) (*Run, error) {
	run, ok := f.runs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	copyRun := *run
	return &copyRun, nil
}
func (f *fakeRepo) ListRuns(context.Context, string, int) ([]Run, error) { return nil, nil }
func (f *fakeRepo) ListTargets(_ context.Context, runID string) ([]Target, error) {
	return append([]Target(nil), f.targets[runID]...), nil
}
func (f *fakeRepo) ListArtifacts(_ context.Context, runID string) ([]Artifact, error) {
	return append([]Artifact(nil), f.artifacts[runID]...), nil
}
func (f *fakeRepo) ListPublications(context.Context, string) ([]Publication, error) {
	return f.publication, nil
}
func (f *fakeRepo) ListEvents(_ context.Context, runID string, after int64, limit int) ([]Event, error) {
	var out []Event
	for _, e := range f.events {
		if e.RunID == runID && e.ID > after {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeRepo) UpdateRunStatus(_ context.Context, id string, from, to RunStatus, _, _ string) (bool, error) {
	run, ok := f.runs[id]
	if !ok || run.Status != from {
		return false, nil
	}
	run.Status = to
	return true, nil
}
func (f *fakeRepo) AppendEvent(_ context.Context, e Event) error {
	f.nextEvent++
	e.ID = f.nextEvent
	f.events = append(f.events, e)
	return nil
}
func (f *fakeRepo) ListExecutors(context.Context) ([]Executor, error) { return nil, nil }
func (f *fakeRepo) GetTarget(_ context.Context, runID, targetID string) (*Target, error) {
	for i := range f.targets[runID] {
		if f.targets[runID][i].TargetID == targetID {
			copyTarget := f.targets[runID][i]
			return &copyTarget, nil
		}
	}
	return nil, errors.New("target not found")
}
func (f *fakeRepo) RetryReleaseV2Target(_ context.Context, runID, targetID string) (bool, error) {
	for i := range f.targets[runID] {
		t := &f.targets[runID][i]
		if t.TargetID == targetID && t.Status == TargetFailed {
			t.Status = TargetRetryQueued
			t.Attempt++
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) UpdateRunNotes(_ context.Context, runID, notes, sha string) (bool, error) {
	run, ok := f.runs[runID]
	if !ok || TerminalRunStatus(run.Status) {
		return false, nil
	}
	run.NotesMD = notes
	run.NotesSHA256 = sha
	return true, nil
}
func (f *fakeRepo) AppendLogChunk(_ context.Context, runID, targetID, chunk string, seq int, actor string) error {
	return f.AppendEvent(context.Background(), Event{RunID: runID, TargetID: targetID, Kind: "target.log",
		Actor: actor, Detail: map[string]any{"seq": float64(seq), "chunk": chunk}})
}
func (f *fakeRepo) ListLogChunks(ctx context.Context, runID, targetID string, after int64, limit int) ([]Event, error) {
	var out []Event
	for _, e := range f.events {
		if e.RunID == runID && e.Kind == "target.log" && e.ID > after && (targetID == "" || e.TargetID == targetID) {
			out = append(out, e)
		}
	}
	return out, nil
}

func TestDispatchBuildTransitionsOnce(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(Run{ID: "run_1", Status: RunQueued})
	svc := NewService(repo)

	run, err := svc.DispatchBuild(context.Background(), "run_1", "agent:t1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunPreparing {
		t.Fatalf("status = %s", run.Status)
	}
	again, err := svc.DispatchBuild(context.Background(), "run_1", "agent:t1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != RunPreparing {
		t.Fatalf("idempotent dispatch changed status to %s", again.Status)
	}
	if len(repo.events) != 1 || repo.events[0].Kind != "build.dispatched" {
		t.Fatalf("events = %+v", repo.events)
	}
}

func TestDispatchBuildRejectsFinishedRun(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(Run{ID: "run_1", Status: RunPublished})
	if _, err := NewService(repo).DispatchBuild(context.Background(), "run_1", "agent"); !errors.Is(err, ErrState) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryTargetRequiresFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(Run{ID: "run_1", Status: RunBuilding},
		Target{TargetID: "darwin-arm64", Status: TargetReady, Attempt: 1},
		Target{TargetID: "darwin-amd64", Status: TargetFailed, Attempt: 1},
	)
	svc := NewService(repo)
	if _, err := svc.RetryTarget(context.Background(), "run_1", "darwin-arm64", "agent", ""); !errors.Is(err, ErrState) {
		t.Fatalf("ready target retry err = %v", err)
	}
	target, err := svc.RetryTarget(context.Background(), "run_1", "darwin-amd64", "agent", "retry flake")
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != TargetRetryQueued || target.Attempt != 2 {
		t.Fatalf("target = %+v", target)
	}
}

func TestRetryTargetResumesFailedRun(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(Run{ID: "run_1", Status: RunFailed},
		Target{TargetID: "linux-amd64", Status: TargetFailed, Attempt: 1},
	)
	svc := NewService(repo)
	target, err := svc.RetryTarget(context.Background(), "run_1", "linux-amd64", "agent", "retry after fix")
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != TargetRetryQueued || target.Attempt != 2 {
		t.Fatalf("target = %+v", target)
	}
	if got := repo.runs["run_1"].Status; got != RunPreparing {
		t.Fatalf("run status = %s, want preparing", got)
	}
}

func TestSaveNotesAndRejectTerminal(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(Run{ID: "run_1", Status: RunBuilding})
	svc := NewService(repo)
	run, err := svc.SaveNotes(context.Background(), "run_1", "  ships fix #12  ", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if run.NotesMD != "ships fix #12" || !strings.HasPrefix(run.NotesSHA256, "sha256:") {
		t.Fatalf("run notes = %q / %q", run.NotesMD, run.NotesSHA256)
	}
	repo.runs["run_1"].Status = RunPublished
	if _, err := svc.SaveNotes(context.Background(), "run_1", "late", "agent"); !errors.Is(err, ErrState) {
		t.Fatalf("terminal notes err = %v", err)
	}
}

func TestLogsOrderAndCursor(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(Run{ID: "run_1", Status: RunBuilding}, Target{TargetID: "darwin-arm64", Status: TargetBuilding})
	svc := NewService(repo)
	for i, chunk := range []string{"first\n", "second\n", "third\n"} {
		if err := svc.AppendLogChunk(context.Background(), "run_1", "darwin-arm64", chunk, i+1, "executor:mac"); err != nil {
			t.Fatal(err)
		}
	}
	chunks, err := svc.Logs(context.Background(), "run_1", "darwin-arm64", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0].Chunk != "first\n" || chunks[2].Seq != 3 {
		t.Fatalf("chunks = %+v", chunks)
	}
	next, err := svc.Logs(context.Background(), "run_1", "darwin-arm64", chunks[1].EventID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Chunk != "third\n" {
		t.Fatalf("cursor chunks = %+v", next)
	}
}
