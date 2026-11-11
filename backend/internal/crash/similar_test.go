package crash

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStackSimilarityIgnoresLinesAndVersions(t *testing.T) {
	a := sampleBundle()
	b := sampleBundle()
	b.Version = "4.3.0"
	b.Exception.Stack = "at Login.Form.Show() in C:\\work\\src\\Login.cs:line 912\nat Login.Main() in C:\\work\\src\\Main.cs:line 3"
	if got := StackSimilarity(a, b); got < 0.99 {
		t.Fatalf("same stack different lines should be ~1, got %.2f", got)
	}

	// 异常类型不同但栈一致（包装异常）：打折但仍可提示。
	c := sampleBundle()
	c.Exception.Type = "InvalidOperationException"
	if got := StackSimilarity(a, c); got <= 0 || got >= 0.99 {
		t.Fatalf("different type with same stack = %.2f, want discounted", got)
	}

	// 完全不同的问题不给分。
	d := sampleBundle()
	d.Exception.Type = "TimeoutException"
	d.Exception.Stack = "at Network.Send() in C:\\work\\Net.cs:line 1\nat Socket.Write() in C:\\work\\Sock.cs:line 2"
	if got := StackSimilarity(a, d); got != 0 {
		t.Fatalf("unrelated stacks should be 0, got %.2f", got)
	}
}

func TestSimilarSectionRendersLinks(t *testing.T) {
	out := SimilarSection([]Similar{{IssueNumber: 12, IssueURL: "https://example.test/issues/12", Fingerprint: "sha256:abcdef0123456789", Version: "4.1", Score: 0.75}})
	if !strings.Contains(out, "[#12](https://example.test/issues/12)") || !strings.Contains(out, "75%") {
		t.Fatalf("section = %s", out)
	}
	if !strings.Contains(out, "人工确认") {
		t.Fatalf("section must warn about manual review: %s", out)
	}
	if SimilarSection(nil) != "" {
		t.Fatal("empty similar must render nothing")
	}
}

func TestIngestAddsSimilarSection(t *testing.T) {
	st := testStore(t)
	_, _ = st.DB().Exec("DELETE FROM crash_reports WHERE user_id LIKE 'test-sim-%'")
	forge := &fakeForge{}
	now := time.Now().UTC()
	svc := &Service{Store: st, Forge: func(context.Context, *string) (Forge, error) { return forge, nil },
		Now: func() time.Time { return now }, RateLimit: 5}

	first := sampleBundle()
	first.UserID = "test-sim-a"
	res, err := svc.Ingest(context.Background(), Ingest{Repo: "example/app", Bundle: first, RawSize: 512,
		ChatID: "grp-sim-1", MessageID: "msg-sim-1"})
	if err != nil || res.Status != "awaiting_confirmation" {
		t.Fatalf("first ingest: %v %+v", err, res)
	}
	draft, _ := st.LatestPendingIssueDraft(context.Background(), "", "grp-sim-1", now)
	if draft == nil || strings.Contains(draft.Body, "可能相关的问题") {
		t.Fatal("first report should not reference anything")
	}
	// 模拟第一份崩溃已完成群内确认，后续相似崩溃才应引用它的 Issue。
	if err := st.UpdateCrashIssue(context.Background(), res.Report.ID, 901, "https://example.test/issues/901", "created", ""); err != nil {
		t.Fatal(err)
	}

	// 同栈不同异常类型、不同版本：应在新 Issue 草稿正文里指出历史 Issue。
	second := sampleBundle()
	second.UserID = "test-sim-b"
	second.Version = "4.3.0"
	second.Exception.Type = "InvalidOperationException"
	second.Exception.Message = "wrapped failure on login"
	res, err = svc.Ingest(context.Background(), Ingest{Repo: "example/app", Bundle: second, RawSize: 512,
		ChatID: "grp-sim-2", MessageID: "msg-sim-2"})
	if err != nil || res.Status != "awaiting_confirmation" {
		t.Fatalf("second ingest: %v %+v", err, res)
	}
	if len(res.Similar) != 1 {
		t.Fatalf("similar = %+v", res.Similar)
	}
	if res.Similar[0].IssueNumber != 901 {
		t.Fatalf("similar issue = %+v", res.Similar)
	}
	draft, _ = st.LatestPendingIssueDraft(context.Background(), "", "grp-sim-2", now)
	if draft == nil || !strings.Contains(draft.Body, "可能相关的问题") || !strings.Contains(draft.Body, "#901") {
		t.Fatalf("body missing similar section: %+v", draft)
	}
	if len(forge.created) != 0 {
		t.Fatalf("forge must not create issue before confirmation: %d", len(forge.created))
	}
	_, _ = st.DB().Exec("DELETE FROM crash_reports WHERE user_id LIKE 'test-sim-%'")
}
