package releasev2

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildPlanNormalizesAndHashes(t *testing.T) {
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	req := PlanRequest{
		ProjectID: "prj_1", Mode: ModePublish, Version: "v0.5.16",
		ResolvedCommit: "4e5ed363519a8e9f9edebf7f800f48b5c9837fec",
		Targets: []TargetPlan{
			{ID: "darwin-arm64", ExecutorID: "mac-intel-01"},
			{ID: "windows-amd64", OS: "windows", Arch: "amd64", ExecutorID: "win-01"},
		},
		Candidates: []CandidateIssue{{Number: 12, Title: "b"}, {Number: 3, Title: "a"}},
	}
	plan, err := BuildPlan(req, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != "0.5.16" || plan.Tag != "v0.5.16" {
		t.Fatalf("version/tag = %s/%s", plan.Version, plan.Tag)
	}
	if plan.Targets[0].ID != "darwin-arm64" || plan.Targets[1].ID != "windows-amd64" {
		t.Fatalf("targets not sorted: %+v", plan.Targets)
	}
	if plan.Candidates[0].Number != 3 {
		t.Fatalf("candidates not sorted: %+v", plan.Candidates)
	}
	if !strings.HasPrefix(plan.PlanSHA256, "sha256:") {
		t.Fatalf("missing plan hash: %q", plan.PlanSHA256)
	}
}

func TestBuildPlanRejectsDuplicateAndMismatchedTargets(t *testing.T) {
	cases := []PlanRequest{
		{ProjectID: "p", Version: "1.2.3", Targets: []TargetPlan{{ID: "windows-amd64"}, {ID: "windows-amd64"}}},
		{ProjectID: "p", Version: "1.2.3", Targets: []TargetPlan{{ID: "darwin-arm64", OS: "windows"}}},
		{ProjectID: "p", Version: "1.2.3", Targets: nil},
	}
	for i, req := range cases {
		if _, err := BuildPlan(req, time.Now()); err == nil {
			t.Fatalf("case %d accepted: %+v", i, req)
		}
	}
}

func TestPlanValidationAndTransitions(t *testing.T) {
	p := &Plan{Status: PlanPlanned, ExpiresAt: time.Now().Add(time.Minute)}
	if err := p.ValidateForRun(time.Now()); err != nil {
		t.Fatal(err)
	}
	p.Blockers = []Blocker{{Code: "missing", Message: "x"}}
	if !errors.Is(p.ValidateForRun(time.Now()), ErrPlanBlocked) {
		t.Fatal("blocked plan accepted")
	}
	if !CanTransition(RunQueued, RunPreparing) || !CanTransition(RunBuilding, RunStaged) {
		t.Fatal("expected transitions missing")
	}
	if CanTransition(RunPublished, RunBuilding) {
		t.Fatal("published run can restart")
	}
	if !TerminalRunStatus(RunPublished) || TerminalRunStatus(RunBuilding) {
		t.Fatal("terminal status mismatch")
	}
}
func TestAgentTokenAndScopes(t *testing.T) {
	raw, token, err := NewAgentToken("rrun_1", "prj_1", []string{"release.read", "artifact.*"}, 2*time.Hour, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 20 || token.TokenHash == raw || !strings.HasPrefix(raw, "rmt_") {
		t.Fatalf("bad token: raw=%q token=%+v", raw, token)
	}
	if !HasScope(token.Scopes, "artifact.verify") || HasScope(token.Scopes, "release.publish") {
		t.Fatalf("scope matching failed: %v", token.Scopes)
	}
}

func TestApprovalPolicyDefaultsAndOverrides(t *testing.T) {
	if required, _ := ApprovalPolicy(nil, "stable"); !required {
		t.Fatal("stable should require approval by default")
	}
	if required, _ := ApprovalPolicy(nil, "beta"); required {
		t.Fatal("beta should not require approval by default")
	}
	policies := map[string]any{"approvals": map[string]any{
		"required_channels": []any{"test", "stable"},
		"ttl":               "30m",
	}}
	required, ttl := ApprovalPolicy(policies, "test")
	if !required || ttl.Minutes() != 30 {
		t.Fatalf("policy = %v %v", required, ttl)
	}
	if required, _ := ApprovalPolicy(policies, "beta"); required {
		t.Fatal("beta should stay unapproved-free")
	}
}
