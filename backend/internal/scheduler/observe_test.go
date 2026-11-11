package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"
)

func nonAtMsg(id, text string) Inbound {
	m := msg(id, text)
	m.IsAtMe = false
	return m
}

func messageStatusByID(t *testing.T, h *harness, id string) MessageRecord {
	t.Helper()
	items, err := h.repo.ListMessages(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.PlatformMessageID == id {
			return item
		}
	}
	t.Fatalf("message %s not recorded", id)
	return MessageRecord{}
}

func TestAtOnlyIgnoresNonAtMessage(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "at_only"}
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-at-only", "导出偶发少几行")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.runner.count("chat") != 0 || len(h.notifier.all()) != 0 {
		t.Fatalf("at_only should be silent, chats=%d replies=%#v", h.runner.count("chat"), h.notifier.all())
	}
	if got := messageStatusByID(t, h, "m-at-only"); got.Status != "ignored" {
		t.Fatalf("status = %+v", got)
	}
}

func TestSmartShadowRecordsDecisionWithoutReply(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "smart", Shadow: true, AnswerConfidence: 0.5}
	h.runner.triageReply = `{"action":"answer","reply":"这个我来看看","confidence":0.9,"reason":"明确指向项目"}`
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-shadow", "这个导出问题是不是你处理？")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(h.notifier.all()) != 0 {
		t.Fatalf("shadow mode must not reply: %#v", h.notifier.all())
	}
	if h.runner.last().BaseInstructions != ObserveSystemPrompt {
		t.Fatalf("expected observe prompt, got %q", h.runner.last().BaseInstructions)
	}
	got := messageStatusByID(t, h, "m-shadow")
	if got.Status != "shadow_answer" || !strings.Contains(got.Reason, "confidence=0.90") {
		t.Fatalf("status = %+v", got)
	}
}

func TestSmartLiveAnswerHonorsHourlyLimit(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "smart", HourlyReplyLimit: 1, ReplyCooldown: time.Second, AnswerConfidence: 0.5}
	h.runner.triageReply = `{"action":"answer","reply":"建议先看日志","confidence":0.9,"reason":"项目问题"}`
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-live-1", "导出报错了怎么办？")); err != nil {
		t.Fatalf("handle 1: %v", err)
	}
	if len(h.notifier.all()) != 1 {
		t.Fatalf("first auto reply missing: %#v", h.notifier.all())
	}
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-live-2", "导入也报错了怎么办？")); err != nil {
		t.Fatalf("handle 2: %v", err)
	}
	if len(h.notifier.all()) != 1 {
		t.Fatalf("second reply should be rate limited: %#v", h.notifier.all())
	}
	if got := messageStatusByID(t, h, "m-live-2"); got.Status != "rate_limited" {
		t.Fatalf("status = %+v", got)
	}
}

func TestNonAtNaturalLanguageControlIsBlocked(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "smart", Shadow: false, AnswerConfidence: 0.1}
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-control", "暂停 #210")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.runner.count("chat") != 0 || len(h.notifier.all()) != 0 {
		t.Fatalf("control should be blocked silently, chats=%d replies=%#v", h.runner.count("chat"), h.notifier.all())
	}
	if got := messageStatusByID(t, h, "m-control"); got.Status != "control_blocked" {
		t.Fatalf("status = %+v", got)
	}

	h.runner.triageReply = `{"action":"silent","reason":"普通缺陷讨论","confidence":0.4}`
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-control-bug", "暂停按钮没反应")); err != nil {
		t.Fatalf("bug handle: %v", err)
	}
	if got := messageStatusByID(t, h, "m-control-bug"); got.Status == "control_blocked" {
		t.Fatalf("a normal bug report must not be blocked as control: %+v", got)
	}

	h.runner.chatReply = `{"action":"answer","reply":"收到"}`
	at := msg("m-control-at", "暂停 #210")
	if err := h.s.HandleMessage(context.Background(), at); err != nil {
		t.Fatalf("at handle: %v", err)
	}
	if len(h.notifier.all()) == 0 || !strings.Contains(strings.Join(h.notifier.all(), "\n"), "没有进行中的 worker") {
		t.Fatalf("explicit @ should reach control: %#v", h.notifier.all())
	}
}

func TestCrossBotClaimPreventsDuplicateReply(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "at_only"}
	h.runner.chatReply = `{"action":"answer","reply":"唯一回复"}`
	first := msg("m-claim", "怎么开启自动同步？")
	first.BotID = "bot-1"
	if err := h.s.HandleMessage(context.Background(), first); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := first
	second.BotID = "bot-2"
	if err := h.s.HandleMessage(context.Background(), second); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(h.notifier.all()) != 1 {
		t.Fatalf("expected one reply across bots, got %#v", h.notifier.all())
	}
}

func TestAutoTriageStillNeedsConfirmationAndFlag(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "smart", AutoIssueEnabled: false, AutoIssueConfidence: 0.85, HourlyReplyLimit: 10}
	h.runner.triageReply = `{"action":"triage","title":"导出偶发少行","body":"100 行只导入 97 行","severity":"high","confidence":0.95,"reason":"具体缺陷"}`
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-triage-off", "导出偶发少三行")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := messageStatusByID(t, h, "m-triage-off"); got.Status != "triage_candidate" {
		t.Fatalf("status = %+v", got)
	}
	if h.tracker.issueCount() != 0 || len(h.notifier.all()) != 0 {
		t.Fatalf("auto issue disabled should do nothing")
	}

	h.project.Policy.AutoIssueEnabled = true
	h.runner.triageReply = `{"action":"triage","title":"导出偶发少行","body":"100 行只导入 97 行","severity":"high","confidence":0.95,"reason":"具体缺陷"}`
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-triage-on", "导出偶发少三行")); err != nil {
		t.Fatalf("handle on: %v", err)
	}
	if h.tracker.issueCount() != 0 {
		t.Fatal("auto triage must still wait for group confirmation")
	}
	if sent := strings.Join(h.notifier.all(), "\n"); !strings.Contains(sent, "待确认建单") {
		t.Fatalf("draft prompt missing: %s", sent)
	}
	count, _ := h.repo.CountAutoIssueDrafts(context.Background(), "prj-1", "group-1", time.Now().Add(-24*time.Hour))
	if count != 1 {
		t.Fatalf("auto drafts = %d", count)
	}
}
