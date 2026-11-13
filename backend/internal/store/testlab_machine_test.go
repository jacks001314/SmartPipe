package store

import (
	"context"
	"errors"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopMachineFencing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("machine-test")})
	if err != nil {
		t.Fatal(err)
	}
	machine := newID("m")
	token := strings.Repeat("x", 64)
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
		_, _ = s.db.Exec("DELETE FROM desktop_test_runs WHERE project_id=$1", p.ID)
	})
	if err = s.RegisterDesktopMachine(ctx, machine, "windows-amd64", testlab.TokenDigest(token)); err != nil {
		t.Fatal(err)
	}
	if s.AuthenticateDesktopMachine(ctx, machine, token) != nil {
		t.Fatal("valid auth failed")
	}
	if s.AuthenticateDesktopMachine(ctx, machine, strings.Repeat("y", 64)) == nil {
		t.Fatal("wrong token accepted")
	}
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64), Scenario: "desktop-observe", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"desktop_observed"}}
	id := newID("r")
	if _, err = s.CreateDesktopTestRun(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimDesktopRun(ctx, machine)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Run.ID != id {
		t.Fatal("wrong assignment")
	}
	if _, err = s.ClaimDesktopRun(ctx, machine); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("double claim: %v", err)
	}
	if _, err = s.HeartbeatDesktopRun(ctx, machine, id, lease.Generation+1); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("wrong generation: %v", err)
	}
	if _, err = s.HeartbeatDesktopRun(ctx, machine, id, lease.Generation); err != nil {
		t.Fatal(err)
	}
	report := testlab.Report{SourceSHA: spec.SourceSHA, ArtifactSHA256: spec.ArtifactSHA256, ScenarioVersion: "1", Platform: spec.Platform, Checks: []testlab.Check{{ID: "desktop_observed", Status: "PASS", Evidence: "screenshot.png"}}}
	if _, err = s.CompleteDesktopTestRun(ctx, id, report); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("admin bypassed fencing: %v", err)
	}
	if _, err = s.db.Exec("UPDATE desktop_test_machines SET lease_until=now()-interval '1 second' WHERE id=$1", machine); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishDesktopRun(ctx, machine, id, lease.Generation, report); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("expired report: %v", err)
	}
	if _, err = s.HeartbeatDesktopRun(ctx, machine, id, lease.Generation); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("expired renewal: %v", err)
	}
	if _, err = s.ClaimDesktopRun(ctx, machine); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("expired machine reused: %v", err)
	}
	if err = s.CancelDesktopTestRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseDesktopMachine(ctx, machine, id, lease.Generation+1); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("wrong release: %v", err)
	}
	if err = s.ReleaseDesktopMachine(ctx, machine, id, lease.Generation); err != nil {
		t.Fatal(err)
	}
}
