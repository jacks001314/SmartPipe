package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopEvidenceRequiredForMachinePass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("evidence")})
	if err != nil {
		t.Fatal(err)
	}
	machine := newID("m")
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
		_, _ = s.db.Exec("DELETE FROM desktop_test_runs WHERE project_id=$1", p.ID)
	})
	if err = s.RegisterDesktopMachine(ctx, machine, "windows-amd64", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
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
	report := testlab.Report{SourceSHA: spec.SourceSHA, ArtifactSHA256: spec.ArtifactSHA256, ScenarioVersion: "1", Platform: spec.Platform, Checks: []testlab.Check{{ID: "desktop_observed", Status: "PASS", Evidence: "local/screen.png"}}}
	if _, err = s.FinishDesktopRun(ctx, machine, id, lease.Generation, report); err == nil {
		t.Fatal("local evidence accepted")
	}
	data := []byte("record")
	sum := sha256.Sum256(data)
	evidence := DesktopEvidence{SHA256: hex.EncodeToString(sum[:]), MIMEType: "text/plain", Payload: data}
	if _, err = s.PutDesktopEvidence(ctx, machine, id, lease.Generation+1, evidence); err == nil {
		t.Fatal("wrong generation accepted")
	}
	ref, err := s.PutDesktopEvidence(ctx, machine, id, lease.Generation, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := s.PutDesktopEvidence(ctx, machine, id, lease.Generation, evidence); err != nil || repeated != ref {
		t.Fatal("idempotent upload failed", err)
	}
	got, err := s.GetDesktopEvidence(ctx, id, evidence.SHA256)
	if err != nil || string(got.Payload) != string(data) {
		t.Fatal("readback failed", err)
	}
	report.Checks[0].Evidence = ref
	run, err := s.FinishDesktopRun(ctx, machine, id, lease.Generation, report)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "PASS" {
		t.Fatal(run.Status)
	}
	if _, err = s.PutDesktopEvidence(ctx, machine, id, lease.Generation, evidence); err == nil {
		t.Fatal("terminal upload accepted")
	}
}
