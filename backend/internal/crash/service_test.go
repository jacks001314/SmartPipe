package crash

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

type fakeForge struct {
	created  []string
	comments []string
	next     int
}

func (f *fakeForge) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*github.Issue, error) {
	f.next++
	f.created = append(f.created, title+"\n"+body)
	return &github.Issue{Number: 900 + f.next, HTMLURL: "https://example.test/issues/900"}, nil
}

func (f *fakeForge) Comment(ctx context.Context, repo string, number int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("SMARTPIPE_TEST_DSN")
	if dsn == "" {
		t.Skip("SMARTPIPE_TEST_DSN 未设置，跳过集成测试")
	}
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i + 3)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, dsn, box)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), "DELETE FROM crash_reports WHERE user_id LIKE 'test-%'")
		_ = st.Close()
	})
	return st
}

func TestIngestCreatesAndDedupes(t *testing.T) {
	st := testStore(t)
	forge := &fakeForge{}
	now := time.Now().UTC()
	svc := &Service{Store: st, Forge: func(context.Context, *string) (Forge, error) { return forge, nil },
		Now: func() time.Time { return now }, RateLimit: 5}

	b := sampleBundle()
	b.UserID = "test-user-1"
	b.Exception.Message = "failed with ghp_ABCDEFGHIJKLMNOPQRSTUVWX"
	out, err := svc.Ingest(context.Background(), Ingest{Repo: "acme/app", Source: "lanxin", Bundle: b, RawSize: 1024})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if out.Status != "created" || out.IssueNumber <= 0 || out.Report == nil || out.Report.ID == 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if len(forge.created) != 1 {
		t.Fatalf("created = %d", len(forge.created))
	}
	if strings.Contains(forge.created[0], "ghp_ABCDEFGHIJKLMNOPQRSTUVWX") || !strings.Contains(forge.created[0], "[REDACTED]") {
		t.Fatalf("issue body not redacted:\n%s", forge.created[0])
	}
	if !strings.Contains(forge.created[0], "crash_report #"+strconv.FormatInt(out.Report.ID, 10)) {
		t.Fatalf("body should reference report id:\n%s", forge.created[0])
	}

	b2 := sampleBundle()
	b2.UserID = "test-user-1"
	b2.Exception.Message = "failed with ghp_ABCDEFGHIJKLMNOPQRSTUVWX"
	now = now.Add(time.Minute)
	out2, err := svc.Ingest(context.Background(), Ingest{Repo: "acme/app", Bundle: b2, RawSize: 1024})
	if err != nil {
		t.Fatalf("ingest 2: %v", err)
	}
	if out2.Status != "deduped" || out2.Occurrences != 2 || out2.IssueNumber != out.IssueNumber {
		t.Fatalf("outcome2 = %+v", out2)
	}
	if len(forge.created) != 1 || len(forge.comments) != 1 {
		t.Fatalf("created=%d comments=%d", len(forge.created), len(forge.comments))
	}
	if !strings.Contains(forge.comments[0], "第 2 次") {
		t.Fatalf("comment = %s", forge.comments[0])
	}
}

func TestIngestRateLimitAndOversize(t *testing.T) {
	st := testStore(t)
	forge := &fakeForge{}
	now := time.Now().UTC()
	svc := &Service{Store: st, Forge: func(context.Context, *string) (Forge, error) { return forge, nil },
		Now: func() time.Time { return now }, RateLimit: 1, MaxBytes: 1024}

	b := sampleBundle()
	b.UserID = "test-user-2"
	if out, _ := svc.Ingest(context.Background(), Ingest{Repo: "acme/app", Bundle: b, RawSize: 512}); out.Status != "created" {
		t.Fatalf("first = %+v", out)
	}
	b2 := sampleBundle()
	b2.UserID = "test-user-2"
	b2.Exception.Stack = "completely different frame"
	b2.Exception.Message = "rate limited but leaks ghp_ZZZZZZZZZZZZZZZZZZZZZZZZ"
	now = now.Add(time.Minute)
	out, err := svc.Ingest(context.Background(), Ingest{Repo: "acme/app", Bundle: b2, RawSize: 512})
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if out.Status != "rate_limited" {
		t.Fatalf("expected rate_limited, got %+v", out)
	}
	if out.Report == nil || strings.Contains(string(out.Report.Bundle), "ghp_ZZZZ") {
		t.Fatalf("rejected row must be redacted: %s", string(out.Report.Bundle))
	}
	big := sampleBundle()
	big.UserID = "test-user-3"
	now = now.Add(time.Minute)
	out, err = svc.Ingest(context.Background(), Ingest{Repo: "acme/app", Bundle: big, RawSize: 4096})
	if err != nil {
		t.Fatalf("oversize: %v", err)
	}
	if out.Status != "rejected" || !strings.Contains(out.Reason, "too large") {
		t.Fatalf("expected rejected, got %+v", out)
	}
	if len(forge.created) != 1 {
		t.Fatalf("created = %d", len(forge.created))
	}
}
