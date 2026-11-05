package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"smartpipe/backend/internal/secretbox"
)

// 集成测试需要真实 Postgres。设置 SMARTPIPE_TEST_DSN 后运行：
//
//	$env:SMARTPIPE_TEST_DSN="postgres://smartpipe:pwd@192.168.1.99:5432/smartpipe_test?sslmode=disable"
//	go test ./internal/store -count=1
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("SMARTPIPE_TEST_DSN")
	if dsn == "" {
		t.Skip("SMARTPIPE_TEST_DSN 未设置，跳过集成测试")
	}
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i + 7)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, dsn, box)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM credentials")
		_, _ = s.db.Exec("DELETE FROM audit_log")
		_, _ = s.db.Exec("DELETE FROM projects")
		_ = s.Close()
	})
	return s
}

func TestProjectLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, &Project{
		Name:               "SmartPipe Core",
		GitHubRepoURL:      "https://github.com/example/core",
		GitHubIssueRepo:    "example/core",
		BluexGroupIDs:      "grp-1,grp-2",
		CodeownersRequired: true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" || p.GitHubDefaultBranch != "main" || p.LabelsPrefix != "smartpipe" {
		t.Fatalf("defaults not applied: %+v", p)
	}

	got, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != p.Name {
		t.Fatalf("name mismatch: %q vs %q", got.Name, p.Name)
	}

	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 project, got %d", len(list))
	}

	got.Name = "SmartPipe Core Renamed"
	got.GitHubDefaultBranch = "develop"
	updated, err := s.UpdateProject(ctx, got)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "SmartPipe Core Renamed" || updated.GitHubDefaultBranch != "develop" {
		t.Fatalf("update not applied: %+v", updated)
	}

	disabled, err := s.SetProjectEnabled(ctx, p.ID, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("project should be disabled")
	}

	if _, err := s.CreateProject(ctx, &Project{Name: "smartpipe core renamed"}); err == nil {
		t.Fatal("expected duplicate name conflict")
	}

	if _, err := s.GetProject(ctx, "prj_missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCredentialLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	proj, err := s.CreateProject(ctx, &Project{Name: "Cred Project", Enabled: true})
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	cred, err := s.CreateCredential(ctx, &proj.ID, CredentialGitHubPAT, "repo-pat", "ghp_1234567890abcdef", map[string]any{"api_base": "https://api.github.com"})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if cred.Mask != "****cdef" {
		t.Fatalf("unexpected mask: %q", cred.Mask)
	}

	var stored string
	if err := s.db.QueryRow(`SELECT ciphertext FROM credentials WHERE id = $1`, cred.ID).Scan(&stored); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if stored == "ghp_1234567890abcdef" {
		t.Fatal("plaintext persisted in database")
	}

	kind, secret, meta, err := s.GetSecret(ctx, cred.ID)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if kind != CredentialGitHubPAT || secret != "ghp_1234567890abcdef" || meta["api_base"] == nil {
		t.Fatalf("secret roundtrip mismatch: %s %s %v", kind, secret, meta)
	}

	rotated, err := s.RotateCredential(ctx, cred.ID, "ghp_newvalue_9999")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Mask != "****9999" {
		t.Fatalf("rotate mask: %q", rotated.Mask)
	}
	_, secret2, _, _ := s.GetSecret(ctx, cred.ID)
	if secret2 != "ghp_newvalue_9999" {
		t.Fatalf("rotation did not take effect: %q", secret2)
	}

	if _, err := s.SetCredentialEnabled(ctx, cred.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, _, _, err := s.GetSecret(ctx, cred.ID); err != ErrNotFound {
		t.Fatalf("disabled credential should be unavailable, got %v", err)
	}

	if _, err := s.CreateCredential(ctx, &proj.ID, "bogus_kind", "x", "y", nil); err == nil {
		t.Fatal("expected unsupported kind error")
	}
	if _, err := s.CreateCredential(ctx, &proj.ID, CredentialGitHubPAT, "repo-pat", "duplicate", nil); err == nil {
		t.Fatal("expected unique conflict")
	}

	if err := s.DeleteCredential(ctx, cred.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteCredential(ctx, cred.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestAuditAppendAndList(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.AppendAudit(ctx, AuditEntry{
			Actor:      "tester",
			Action:     "project.create",
			TargetType: "project",
			TargetID:   "prj_x",
			Result:     "ok",
			Detail:     map[string]any{"i": i},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	entries, err := s.ListAudit(ctx, 10, "project")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].At.Before(entries[2].At) {
		t.Fatal("audit not sorted newest first")
	}
	if entries[0].TraceID == "" {
		t.Log("trace id empty (allowed for manual append)")
	}
}

// 蓝信群是入口，配置台里一个项目可以绑多个群；反查必须按逗号分词、忽略空白与大小写。
func TestFindProjectByGroup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, &Project{
		Name:            "group-lookup",
		GitHubRepoURL:   "https://github.com/example/group",
		GitHubIssueRepo: "example/group",
		BluexGroupIDs:   " grp-alpha , GRP-Beta ,,grp-gamma",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, group := range []string{"grp-alpha", "grp-beta", "grp-gamma", " GRP-ALPHA "} {
		got, err := s.FindProjectByGroup(ctx, group)
		if err != nil {
			t.Fatalf("lookup %q: %v", group, err)
		}
		if got.ID != p.ID {
			t.Fatalf("lookup %q = %s, want %s", group, got.ID, p.ID)
		}
	}

	if _, err := s.FindProjectByGroup(ctx, "grp-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group should be ErrNotFound, got %v", err)
	}
	if _, err := s.FindProjectByGroup(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty group should be ErrNotFound, got %v", err)
	}
}