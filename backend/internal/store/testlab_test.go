package store

import (
	"context"
	"errors"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopTestRunLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: "desktop-test-" + newID("p")})
	if err != nil {
		t.Fatal(err)
	}
	id := newID("test")
	t.Cleanup(func() { _, _ = s.db.Exec("DELETE FROM desktop_test_runs WHERE project_id=$1", p.ID) })
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64), Scenario: "desktop", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"ui"}}
	r, err := s.CreateDesktopTestRun(ctx, id, spec)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "QUEUED" {
		t.Fatal(r.Status)
	}
	report := testlab.Report{SourceSHA: spec.SourceSHA, ArtifactSHA256: spec.ArtifactSHA256, ScenarioVersion: spec.ScenarioVersion, Platform: spec.Platform, Checks: []testlab.Check{{ID: "ui", Status: "PASS", Evidence: "run/screen.png"}}}
	if _, err = s.CompleteDesktopTestRun(ctx, id, report); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("unstarted report: %v", err)
	}
	if err = s.StartDesktopTestRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = s.StartDesktopTestRun(ctx, id); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("double start: %v", err)
	}
	r, err = s.CompleteDesktopTestRun(ctx, id, report)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "PASS" || r.FinishedAt == nil {
		t.Fatal("not finalized")
	}
	if _, err = s.CompleteDesktopTestRun(ctx, id, report); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("terminal overwritten: %v", err)
	}
	id = newID("test")
	if _, err = s.CreateDesktopTestRun(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	if err = s.StartDesktopTestRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelDesktopTestRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteDesktopTestRun(ctx, id, report); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("late report: %v", err)
	}
}
