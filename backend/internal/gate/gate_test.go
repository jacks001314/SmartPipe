package gate

import (
	"context"
	"testing"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testdb"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := testdb.DSN(t)
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i + 7)
	}
	box, _ := secretbox.New(key)
	st, err := store.Open(context.Background(), dsn, box)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = st.DB().Exec("DELETE FROM issue_approvals")
	_, _ = st.DB().Exec("DELETE FROM issue_plans")
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestParseIssueRef(t *testing.T) {
	repo, num, ok := ParseIssueRef("example/core#42")
	if !ok || repo != "example/core" || num != 42 {
		t.Fatalf("got %q %d %v", repo, num, ok)
	}
	repo, num, ok = ParseIssueRef("#7")
	if !ok || repo != "" || num != 7 {
		t.Fatalf("got %q %d %v", repo, num, ok)
	}
	if _, _, ok := ParseIssueRef("not-an-issue"); ok {
		t.Fatal("should reject")
	}
}

func TestGateAllowsSimpleIssue(t *testing.T) {
	st := testStore(t)
	state, err := Check(context.Background(), st, &store.Project{GitHubIssueRepo: "example/core"}, "example/core#100")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !state.OK || state.Required {
		t.Fatalf("state = %+v", state)
	}
}

func TestGateRequiresApprovalThenPasses(t *testing.T) {
	st := testStore(t)
	project := &store.Project{GitHubIssueRepo: "example/core"}
	body := "1. reproduce\n2. fix\n3. canary"
	hash := bridge.PlanHash(body)
	plan, err := st.CreatePlan(context.Background(), "example/core", 101, body, hash, "lead", 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	state, err := Check(context.Background(), st, project, "example/core#101")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !state.Required || state.OK || state.Version != plan.Version {
		t.Fatalf("state = %+v", state)
	}
	if _, err := st.ApprovePlan(context.Background(), store.IssueApproval{
		Repo: "example/core", IssueNumber: 101, Version: plan.Version, PlanHash: hash, Approver: "owner", CommentID: 2,
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	state, err = Check(context.Background(), st, project, "example/core#101")
	if err != nil || !state.OK || !state.Approved || state.Approver != "owner" {
		t.Fatalf("state = %+v err=%v", state, err)
	}
}

func TestGateRejectsTamperedPlan(t *testing.T) {
	st := testStore(t)
	project := &store.Project{GitHubIssueRepo: "example/core"}
	body := "original"
	plan, err := st.CreatePlan(context.Background(), "example/core", 102, body, bridge.PlanHash(body), "lead", 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := st.DB().Exec("UPDATE issue_plans SET body = 'tampered' WHERE id = $1", plan.ID); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	state, err := Check(context.Background(), st, project, "example/core#102")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if state.OK || state.Approved {
		t.Fatalf("state = %+v", state)
	}
}
