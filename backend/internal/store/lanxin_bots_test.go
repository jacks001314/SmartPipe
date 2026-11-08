package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLanxinBotLifecycleAndProjectGroupRouting(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	name := fmt.Sprintf("lanxin-test-%d", time.Now().UnixNano())
	cred, err := s.CreateCredential(ctx, nil, CredentialLanxin, "bot-"+name, "secret-value", map[string]any{"app_id": "app-1"})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	bot, err := s.CreateLanxinBot(ctx, LanxinBot{
		Name: name, CredentialID: cred.ID, AppID: "app-1", APIBase: "https://example.test",
		RequireAtMe: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if bot.ID == "" || bot.Status != "disconnected" {
		t.Fatalf("unexpected bot: %+v", bot)
	}

	if err := s.ReplaceLanxinGroups(ctx, bot.ID, []LanxinGroup{
		{GroupID: "group-a", Name: "研发群", OwnerName: "Alice", MemberCount: 12, SendMsgStatus: true, Active: true},
		{GroupID: "group-b", Name: "产品群", MemberCount: 8, SendMsgStatus: true, Active: true},
	}); err != nil {
		t.Fatalf("replace groups: %v", err)
	}
	groups, err := s.ListLanxinGroups(ctx, bot.ID, false)
	if err != nil || len(groups) != 2 || groups[0].BotName != name {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	if err := s.SetLanxinBotStatus(ctx, bot.ID, "connected", ""); err != nil {
		t.Fatalf("status: %v", err)
	}
	got, err := s.GetLanxinBot(ctx, bot.ID)
	if err != nil || got.Status != "connected" || got.LastConnectedAt == nil || got.GroupCount != 2 {
		t.Fatalf("get bot = %+v, %v", got, err)
	}

	project, err := s.CreateProject(ctx, &Project{
		Name: name, GitHubIssueRepo: "example/repo", BluexGroupIDs: "group-a", LanxinBotID: bot.ID, Enabled: true,
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	found, err := s.FindProjectByGroupBot(ctx, bot.ID, "group-a")
	if err != nil || found.ID != project.ID {
		t.Fatalf("find exact = %+v, %v", found, err)
	}
	if _, err := s.FindProjectByGroupBot(ctx, "other-bot", "group-a"); err != ErrNotFound {
		t.Fatalf("wrong bot must not match, got %v", err)
	}

	if err := s.ReplaceLanxinGroups(ctx, bot.ID, []LanxinGroup{{GroupID: "group-a", Name: "研发群", Active: true}}); err != nil {
		t.Fatalf("replace again: %v", err)
	}
	active, _ := s.ListLanxinGroups(ctx, bot.ID, false)
	all, _ := s.ListLanxinGroups(ctx, bot.ID, true)
	if len(active) != 1 || len(all) != 2 {
		t.Fatalf("active=%d all=%d", len(active), len(all))
	}

	if err := s.DeleteLanxinBot(ctx, bot.ID); err != nil {
		t.Fatalf("delete bot: %v", err)
	}
	if err := s.DeleteCredential(ctx, cred.ID); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
}
