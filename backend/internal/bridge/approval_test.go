package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/store"
)

func newApprovalService(env *testEnv) (*ApprovalService, *int) {
	notified := new(int)
	svc := &ApprovalService{
		Store: env.store,
		ClientFor: func(ctx context.Context, projectID *string) (*github.Client, error) {
			return github.New(env.gh.URL, "test-token"), nil
		},
		NotifyLead: func(ctx context.Context, projectID, text string) error {
			*notified++
			return nil
		},
	}
	return svc, notified
}

func TestPlanThenApprove(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	svc, notified := newApprovalService(env)

	out, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 42, CommentID: 1,
		Body: "/plan 1. 定位崩溃点\n2. 加回归测试\n3. 灰度发布", User: "lead", Association: "MEMBER",
	})
	if err != nil || out.Action != "plan_created" || out.Version != 1 {
		t.Fatalf("plan: %+v err=%v", out, err)
	}
	if len(env.comments) == 0 || !strings.Contains(env.comments[0], "/approve v1") {
		t.Fatalf("plan reply missing approve hint: %v", env.comments)
	}

	denied, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 42, CommentID: 2,
		Body: "/approve v1", User: "guest", Association: "CONTRIBUTOR",
	})
	if !errors.Is(err, ErrNotMaintainer) || denied.Action != "approve_denied" {
		t.Fatalf("expected not maintainer, got %+v err=%v", denied, err)
	}
	if *notified != 0 {
		t.Fatal("lead must not be notified before approval")
	}

	ok, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 42, CommentID: 3,
		Body: "/approve v1", User: "maintainer", Association: "OWNER",
	})
	if err != nil || ok.Action != "approved" || ok.Version != 1 {
		t.Fatalf("approve: %+v err=%v", ok, err)
	}
	if *notified != 1 {
		t.Fatalf("lead notified %d times, want 1", *notified)
	}
	if _, err := env.store.GetApproval(context.Background(), "example/core", 42, 1); err != nil {
		t.Fatalf("approval not stored: %v", err)
	}

	dup, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 42, CommentID: 4,
		Body: "/approve v1", User: "maintainer", Association: "MEMBER",
	})
	if err != nil || dup.Action != "approve_duplicate" {
		t.Fatalf("duplicate approve: %+v err=%v", dup, err)
	}
	if *notified != 1 {
		t.Fatal("duplicate approval must not notify lead again")
	}
	_ = project
}

func TestApproveRejectsTamperedPlan(t *testing.T) {
	env := newEnv(t)
	env.project(t, true)
	svc, _ := newApprovalService(env)
	if out, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 7, CommentID: 1, Body: "/plan 原方案", User: "lead", Association: "MEMBER",
	}); err != nil || out.Action != "plan_created" {
		t.Fatalf("plan: %+v err=%v", out, err)
	}
	if _, err := env.store.DB().Exec("UPDATE issue_plans SET body = '被改过的方案' WHERE repo = $1 AND issue_number = 7", "example/core"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	out, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 7, CommentID: 2, Body: "/approve v1", User: "owner", Association: "OWNER",
	})
	if out.Action != "approve_hash_mismatch" || err == nil {
		t.Fatalf("expected hash mismatch, got %+v err=%v", out, err)
	}
	if _, err := env.store.GetApproval(context.Background(), "example/core", 7, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approval should not exist, err=%v", err)
	}
}

func TestApproveUnknownVersion(t *testing.T) {
	env := newEnv(t)
	env.project(t, true)
	svc, _ := newApprovalService(env)
	out, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 9, CommentID: 1, Body: "/approve v3", User: "owner", Association: "OWNER",
	})
	if err != nil || out.Action != "approve_missing_plan" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestPlanHashIsStableAcrossLineEndings(t *testing.T) {
	a := PlanHash("line1\r\nline2\r\n")
	b := PlanHash("  line1\nline2  ")
	if a != b {
		t.Fatalf("hash mismatch: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Fatalf("unexpected hash format %q", a)
	}
}

func TestIsMaintainer(t *testing.T) {
	for _, assoc := range []string{"OWNER", "member", "Collaborator"} {
		if !IsMaintainer(assoc) {
			t.Fatalf("%s should be maintainer", assoc)
		}
	}
	for _, assoc := range []string{"CONTRIBUTOR", "NONE", ""} {
		if IsMaintainer(assoc) {
			t.Fatalf("%s should not be maintainer", assoc)
		}
	}
}
