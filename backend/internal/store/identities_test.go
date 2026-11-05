package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestUserIdentityLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.DB().Exec("DELETE FROM user_identities")
	_, _ = s.DB().Exec("DELETE FROM projects WHERE name LIKE 'identity-test-%'")

	global, err := s.UpsertUserIdentity(ctx, UserIdentity{Platform: "Lanxin", PlatformUserID: "u-1", GitHubLogin: "@bob", DisplayName: "Bob", Enabled: true})
	if err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if global.Platform != "lanxin" || global.GitHubLogin != "bob" || global.ProjectID != nil {
		t.Fatalf("normalisation failed: %+v", global)
	}

	// 同键重复写入走更新，不新增。
	again, err := s.UpsertUserIdentity(ctx, UserIdentity{Platform: "lanxin", PlatformUserID: "u-1", GitHubLogin: "bob2", DisplayName: "Bob 2", Enabled: true})
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if again.ID != global.ID || again.GitHubLogin != "bob2" {
		t.Fatalf("upsert should update in place: %+v", again)
	}

	p, err := s.CreateProject(ctx, &Project{Name: fmt.Sprintf("identity-test-%d", time.Now().UnixNano()), Enabled: true})
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	scoped, err := s.UpsertUserIdentity(ctx, UserIdentity{Platform: "lanxin", PlatformUserID: "u-1", ProjectID: &p.ID, GitHubLogin: "carol", Enabled: true})
	if err != nil {
		t.Fatalf("upsert scoped: %v", err)
	}

	// 项目级映射优先于全局。
	found, err := s.FindUserIdentity(ctx, "lanxin", "u-1", &p.ID)
	if err != nil || found.GitHubLogin != "carol" {
		t.Fatalf("find scoped = %+v, %v", found, err)
	}
	other := ""
	found, err = s.FindUserIdentity(ctx, "lanxin", "u-1", &other)
	if err != nil || found.GitHubLogin != "bob2" {
		t.Fatalf("find global = %+v, %v", found, err)
	}

	list, err := s.ListUserIdentities(ctx, "lanxin", nil)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v", len(list), err)
	}

	// 停用后不再命中。
	disabled := false
	if _, err := s.UpsertUserIdentity(ctx, UserIdentity{Platform: "lanxin", PlatformUserID: "u-1", ProjectID: &p.ID, GitHubLogin: "carol", Enabled: disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := s.DeleteUserIdentity(ctx, scoped.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.FindUserIdentity(ctx, "lanxin", "u-1", &p.ID); err != nil {
		t.Fatalf("global should still match: %v", err)
	}
	if err := s.DeleteUserIdentity(ctx, scoped.ID); err != ErrNotFound {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}
