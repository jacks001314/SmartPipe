package store

import (
	"context"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopDisableAndRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("recovery")})
	if err != nil {
		t.Fatal(err)
	}
	id := newID("m")
	token := strings.Repeat("x", 64)
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", id)
		_, _ = s.db.Exec("DELETE FROM desktop_test_runs WHERE project_id=$1", p.ID)
	})
	if err = s.RegisterDesktopMachine(ctx, id, "windows-amd64", testlab.TokenDigest(token)); err != nil {
		t.Fatal(err)
	}
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64), Scenario: "desktop-observe", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"desktop_observed"}}
	run, err := s.CreateDesktopTestRun(ctx, newID("r"), spec)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimDesktopRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetDesktopMachineEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if s.AuthenticateDesktopMachine(ctx, id, token) == nil {
		t.Fatal("disabled auth accepted")
	}
	run, err = s.GetDesktopTestRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "CANCELLED" {
		t.Fatal(run.Status)
	}
	if s.SetDesktopMachineEnabled(ctx, id, true) == nil {
		t.Fatal("busy machine enabled without recovery")
	}
	if s.RecoverDesktopMachine(ctx, id, lease.Generation+1) == nil {
		t.Fatal("wrong generation recovery")
	}
	if err = s.RecoverDesktopMachine(ctx, id, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if err = s.SetDesktopMachineEnabled(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthenticateDesktopMachine(ctx, id, token); err != nil {
		t.Fatal(err)
	}
}
