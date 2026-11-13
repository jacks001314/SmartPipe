package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIssueAutoCreatesAndQueuesWorker(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"issue","title":"导入偶发丢行","body":"导入 100 行只出现 97 行","severity":"high"}`
	if err := h.s.HandleMessage(context.Background(), msg("draft-auto", "导入偶尔少几行")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.tracker.issueCount() != 1 {
		t.Fatalf("issue should be created without confirmation, got %d", h.tracker.issueCount())
	}
	if w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 1); w == nil {
		t.Fatal("created issue should queue a worker")
	}
	sent := strings.Join(h.notifier.all(), "\n")
	if strings.Contains(sent, "【待确认建单】") || !strings.Contains(sent, "已建 Issue #1") {
		t.Fatalf("group should receive the issue link: %q", sent)
	}
}

func TestIssueDraftCancel(t *testing.T) {
	h := newHarness(t, Config{})
	// 自动建单之后，群里能取消的是建单失败留下的草稿。
	draft := &IssueDraft{
		ID: "DRAFT-CANCEL01", Kind: "chat", ProjectID: "prj-1", ChatID: "group-1",
		SourceMessageID: "draft-cancel-source", Title: "取消建单测试", Body: "正文", Severity: "medium",
		Status: IssueDraftPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: time.Now().Add(issueDraftTTL),
	}
	if created, err := h.repo.CreateIssueDraft(context.Background(), draft); err != nil || !created {
		t.Fatalf("create draft: %v created=%v", err, created)
	}
	draftID := draft.ID
	if err := h.s.HandleMessage(context.Background(), msg("draft-cancel", "取消建单 "+draftID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if h.tracker.issueCount() != 0 {
		t.Fatalf("cancelled draft created issue")
	}
	if !strings.Contains(strings.Join(h.notifier.all(), "\n"), "已取消建单草稿") {
		t.Fatalf("cancel reply missing: %#v", h.notifier.all())
	}
}

func TestIssueDraftExpires(t *testing.T) {
	h := newHarness(t, Config{})
	current := time.Now()
	h.s.now = func() time.Time { return current }
	draft := &IssueDraft{
		ID: "DRAFT-EXPIRE01", Kind: "chat", ProjectID: "prj-1", ChatID: "group-1",
		SourceMessageID: "draft-expire-source", Title: "过期测试", Body: "正文", Severity: "medium",
		Status: IssueDraftPending, CreatedAt: current, UpdatedAt: current, ExpiresAt: current.Add(issueDraftTTL),
	}
	if created, err := h.repo.CreateIssueDraft(context.Background(), draft); err != nil || !created {
		t.Fatalf("create draft: %v created=%v", err, created)
	}
	draftID := draft.ID
	current = current.Add(issueDraftTTL + time.Second)
	if err := h.s.HandleMessage(context.Background(), msg("draft-expire", "确认建单 "+draftID)); err != nil {
		t.Fatalf("confirm expired: %v", err)
	}
	if h.tracker.issueCount() != 0 {
		t.Fatalf("expired draft created issue")
	}
	if !strings.Contains(strings.Join(h.notifier.all(), "\n"), "没有待确认的建单草稿") {
		t.Fatalf("expiry reply missing: %#v", h.notifier.all())
	}
}

func TestIssueDraftConfirmationIsIdempotent(t *testing.T) {
	h := newHarness(t, Config{})
	draft := &IssueDraft{
		ID: "DRAFT-IDEMPOT01", Kind: "chat", ProjectID: "prj-1", ChatID: "group-1",
		SourceMessageID: "draft-idempotent-source", Title: "幂等确认测试", Body: "正文", Severity: "medium",
		Status: IssueDraftPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: time.Now().Add(issueDraftTTL),
	}
	if created, err := h.repo.CreateIssueDraft(context.Background(), draft); err != nil || !created {
		t.Fatalf("create draft: %v created=%v", err, created)
	}
	draftID := draft.ID
	for i := 0; i < 2; i++ {
		if err := h.s.HandleMessage(context.Background(), msg("draft-idempotent-confirm", "确认建单 "+draftID)); err != nil {
			t.Fatalf("confirm %d: %v", i, err)
		}
	}
	if h.tracker.issueCount() != 1 {
		t.Fatalf("confirmation should create exactly one issue, got %d", h.tracker.issueCount())
	}
}

func TestParseIssueDraftCommand(t *testing.T) {
	action, id, ok := parseIssueDraftCommand("@机器人 确认建单 draft-0123abcd")
	if !ok || action != "confirm" || id != "DRAFT-0123ABCD" {
		t.Fatalf("parse confirm = %q %q %v", action, id, ok)
	}
	action, _, ok = parseIssueDraftCommand("@机器人 取消建单")
	if !ok || action != "cancel" {
		t.Fatalf("parse cancel = %q %v", action, ok)
	}
	if _, _, ok := parseIssueDraftCommand("确认一下这个建单思路"); ok {
		t.Fatal("ordinary discussion must not be parsed as a command")
	}
}

func TestCrashDraftConfirmationCreatesIssueAndQueuesWorker(t *testing.T) {
	h := newHarness(t, Config{})
	now := time.Now()
	draft := &IssueDraft{
		ID: "DRAFT-C0FFEE01", Kind: "crash", ProjectID: "prj-1", ChatID: "group-1",
		SourceMessageID: "crash-msg-1", Title: "登录页空引用崩溃", Body: "crash_report #42\n\n空引用",
		Severity: "high", RefID: "42", Repo: "acme/core", Labels: []string{"crash", "crash_report", "source:lanxin"},
		Status: IssueDraftPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(issueDraftTTL),
	}
	if created, err := h.repo.CreateIssueDraft(context.Background(), draft); err != nil || !created {
		t.Fatalf("create crash draft: %v created=%v", err, created)
	}
	if err := h.s.HandleMessage(context.Background(), msg("crash-confirm", "确认建单 DRAFT-C0FFEE01")); err != nil {
		t.Fatalf("confirm crash draft: %v", err)
	}
	if h.tracker.issueCount() != 1 {
		t.Fatalf("issue count = %d", h.tracker.issueCount())
	}
	if got := h.tracker.issues[0]; got.Body != draft.Body || len(got.Labels) != 3 || got.Labels[0] != "crash" {
		t.Fatalf("crash issue = %+v", got)
	}
	if w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 1); w == nil {
		t.Fatal("confirmed crash issue should queue a worker")
	}
}
