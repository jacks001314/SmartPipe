package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopReleaseGateRequiresMatchingUploadedProof(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("gate")})
	if err != nil {
		t.Fatal(err)
	}
	machine := newID("m")
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM desktop_gate_policies WHERE project_id=$1", p.ID)
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
		_, _ = s.db.Exec("DELETE FROM desktop_test_runs WHERE project_id=$1", p.ID)
	})
	if err = s.RegisterDesktopMachine(ctx, machine, "windows-amd64", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	policy := DesktopGatePolicy{Scenario: "installed-code-fix", Version: "1", Platform: "windows-amd64", ArtifactRole: "installer", RequiredChecks: []string{"file_test"}}
	if err = s.SetDesktopGatePolicies(ctx, p.ID, []DesktopGatePolicy{policy}); err != nil {
		t.Fatal(err)
	}
	run := &releasev2.Run{ProjectID: p.ID, ResolvedCommit: strings.Repeat("a", 40)}
	targets := []releasev2.Target{{TargetID: "win", OS: "windows", Arch: "amd64"}}
	artifacts := []releasev2.Artifact{{TargetID: "win", Role: "installer", SHA256: strings.Repeat("b", 64), Name: "setup.exe"}}
	if s.CheckDesktopReleaseGate(ctx, run, targets, artifacts) == nil {
		t.Fatal("missing acceptance passed")
	}
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: run.ResolvedCommit, ArtifactSHA256: artifacts[0].SHA256, Scenario: policy.Scenario, ScenarioVersion: "1", Platform: policy.Platform, RequiredChecks: policy.RequiredChecks}
	id := newID("r")
	if _, err = s.CreateDesktopTestRun(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimDesktopRun(ctx, machine, "desktop-observe@1"); err != ErrNotFound {
		t.Fatalf("unsupported scenario claimed: %v", err)
	}
	lease, err := s.ClaimDesktopRun(ctx, machine, "installed-code-fix@1")
	if err != nil {
		t.Fatal(err)
	}
	proof := []byte("independent test fixture proof")
	hash := sha256.Sum256(proof)
	ref, err := s.PutDesktopEvidence(ctx, machine, id, lease.Generation, DesktopEvidence{SHA256: hex.EncodeToString(hash[:]), MIMEType: "text/plain", Payload: proof})
	if err != nil {
		t.Fatal(err)
	}
	report := testlab.Report{SourceSHA: spec.SourceSHA, ArtifactSHA256: spec.ArtifactSHA256, ScenarioVersion: "1", Platform: spec.Platform, Checks: []testlab.Check{{ID: "file_test", Status: "PASS", Evidence: ref}}}
	if _, err = s.FinishDesktopRun(ctx, machine, id, lease.Generation, report); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckDesktopReleaseGate(ctx, run, targets, artifacts); err != nil {
		t.Fatal(err)
	}
	artifacts[0].SHA256 = strings.Repeat("c", 64)
	if s.CheckDesktopReleaseGate(ctx, run, targets, artifacts) == nil {
		t.Fatal("changed artifact passed")
	}
	artifacts[0].SHA256 = spec.ArtifactSHA256
	policy.RequiredChecks = append(policy.RequiredChecks, "upgrade")
	if err = s.SetDesktopGatePolicies(ctx, p.ID, []DesktopGatePolicy{policy}); err != nil {
		t.Fatal(err)
	}
	if s.CheckDesktopReleaseGate(ctx, run, targets, artifacts) == nil {
		t.Fatal("expanded policy passed")
	}
}
