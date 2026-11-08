package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/store"
)

func newApprovalService(env *testEnv) *ApprovalService {
	return &ApprovalService{
		Store: env.store,
		ClientFor: func(ctx context.Context, projectID *string) (*github.Client, error) {
			return github.New(env.gh.URL, "test-token"), nil
		},
	}
}

func TestPlanThenApprove(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	svc := newApprovalService(env)

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

	ok, err := svc.HandleComment(context.Background(), CommentEvent{
		Repo: "example/core", IssueNumber: 42, CommentID: 3,
		Body: "/approve v1", User: "maintainer", Association: "OWNER",
	})
	if err != nil || ok.Action != "approved" || ok.Version != 1 {
		t.Fatalf("approve: %+v err=%v", ok, err)
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
	_ = project
}

func TestApproveRejectsTamperedPlan(t *testing.T) {
	env := newEnv(t)
	env.project(t, true)
	svc := newApprovalService(env)
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
	svc := newApprovalService(env)
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

type groupCall struct {
	ChatType string
	Target   string
	Text     string
}

type recordingNotifier struct {
	calls []groupCall
}

func (r *recordingNotifier) Reply(ctx context.Context, botID, chatType, targetID, refMsgID, text string) error {
	r.calls = append(r.calls, groupCall{ChatType: chatType, Target: targetID, Text: text})
	return nil
}

// 方案登记与批准这两个里程碑要同步到项目绑定的蓝信群；重复批准不应重复打扰。
func TestPlanAndApproveNotifyLanxinGroups(t *testing.T) {
	env := newEnv(t)
	const repo = "example/groups"
	project, err := env.store.CreateProject(context.Background(), &store.Project{
		Name:            "bridge-groups",
		GitHubRepoURL:   "https://github.com/" + repo,
		GitHubIssueRepo: repo,
		LabelsPrefix:    "smartpipe",
		BluexGroupIDs:   "grp-a, grp-b",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := env.store.CreateCredential(context.Background(), &project.ID, store.CredentialGitHubPAT, "gh-pat", "ghp_test_token_123456", map[string]any{"api_base": env.gh.URL}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	rec := &recordingNotifier{}
	svc := &ApprovalService{
		Store: env.store,
		ClientFor: func(ctx context.Context, projectID *string) (*github.Client, error) {
			return github.New(env.gh.URL, "test-token"), nil
		},
		GroupNotify: rec,
	}
	base := CommentEvent{Repo: repo, IssueNumber: 7, CommentID: 1}

	plan := base
	plan.Body, plan.User, plan.Association = "/plan 1. 复现\n2. 修复", "lead", "MEMBER"
	if out, err := svc.HandleComment(context.Background(), plan); err != nil || out.Action != "plan_created" {
		t.Fatalf("plan: %+v err=%v", out, err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("expected 2 group messages for plan, got %d: %+v", len(rec.calls), rec.calls)
	}
	if rec.calls[0].ChatType != "group" || rec.calls[0].Target != "grp-a" || rec.calls[1].Target != "grp-b" {
		t.Fatalf("unexpected targets: %+v", rec.calls)
	}
	if !strings.Contains(rec.calls[0].Text, "#7") || !strings.Contains(rec.calls[0].Text, "/approve v1") {
		t.Fatalf("plan notice missing issue or command: %q", rec.calls[0].Text)
	}

	approve := base
	approve.CommentID = 2
	approve.Body, approve.User, approve.Association = "/approve v1", "maintainer", "OWNER"
	if out, err := svc.HandleComment(context.Background(), approve); err != nil || out.Action != "approved" {
		t.Fatalf("approve: %+v err=%v", out, err)
	}
	if len(rec.calls) != 4 {
		t.Fatalf("expected 4 group messages after approve, got %d", len(rec.calls))
	}
	if !strings.Contains(rec.calls[2].Text, "\u5df2\u83b7 @maintainer \u6279\u51c6") {
		t.Fatalf("approve notice text: %q", rec.calls[2].Text)
	}

	dup := base
	dup.CommentID = 3
	dup.Body, dup.User, dup.Association = "/approve v1", "maintainer", "OWNER"
	if out, err := svc.HandleComment(context.Background(), dup); err != nil || out.Action != "approve_duplicate" {
		t.Fatalf("duplicate: %+v err=%v", out, err)
	}
	if len(rec.calls) != 4 {
		t.Fatalf("duplicate approve must not notify again, calls=%d", len(rec.calls))
	}
}
