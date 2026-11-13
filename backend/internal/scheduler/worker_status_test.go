package scheduler

import (
	"context"
	"strings"
	"testing"
)

func TestWorkerStatusQuestionUsesActualWorker(t *testing.T) {
	h := newHarness(t, Config{})
	_, _ = h.repo.CreateWorker(context.Background(), &Worker{
		ProjectID: "prj-1", Issue: 210, State: StateCoding, ChatID: "group-1", Branch: "agent/issue-210",
	})
	_ = h.repo.AppendChat(context.Background(), ChatMessage{
		ChatID: "group-1", ProjectID: "prj-1", Role: "assistant", Body: "已补充到 Issue #210",
	})
	h.runner.chatReply = `{"action":"answer","reply":"不应该调用模型"}`

	if err := h.s.HandleMessage(context.Background(), msg("m-status", "有派发worker去修复这个问题吗@XTianTian")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.runner.count("chat") != 0 {
		t.Fatalf("status question should not call chat worker, got %d", h.runner.count("chat"))
	}
	sent := strings.Join(h.notifier.all(), "\n")
	if !strings.Contains(sent, "Issue #210") || !strings.Contains(sent, "正在修改代码") {
		t.Fatalf("status reply = %q", sent)
	}
}

func TestWorkerStatusQuestionWithoutWorker(t *testing.T) {
	h := newHarness(t, Config{})
	_ = h.repo.AppendChat(context.Background(), ChatMessage{
		ChatID: "group-1", ProjectID: "prj-1", Role: "assistant", Body: "已补充到 Issue #210",
	})
	h.runner.chatReply = `{"action":"answer","reply":"不应该调用模型"}`

	if err := h.s.HandleMessage(context.Background(), msg("m-no-worker", "有派发worker去修复这个问题吗")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.runner.count("chat") != 0 {
		t.Fatalf("status question should not call chat worker, got %d", h.runner.count("chat"))
	}
	sent := strings.Join(h.notifier.all(), "\n")
	if !strings.Contains(sent, "目前没有") || !strings.Contains(sent, "Issue #210") {
		t.Fatalf("status reply = %q", sent)
	}
}

func TestChatPromptIncludesWorkerState(t *testing.T) {
	h := newHarness(t, Config{})
	_, _ = h.repo.CreateWorker(context.Background(), &Worker{
		ProjectID: "prj-1", Issue: 5, State: StateVerifying, ChatID: "group-1", Branch: "agent/issue-5",
	})
	h.runner.chatReply = `{"action":"answer","reply":"收到"}`

	if err := h.s.HandleMessage(context.Background(), msg("m-worker-prompt", "补充 #5：日志已附上")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	prompt := h.runner.last().Prompt
	if !strings.Contains(prompt, "Issue #5 当前 worker 状态=正在验证") {
		t.Fatalf("prompt missing worker state: %s", prompt)
	}
}

func TestChatHistoryRemembersCreatedIssue(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"issue","title":"导出按钮点击无响应","body":"点导出没反应","severity":"high"}`
	if err := h.s.HandleMessage(context.Background(), msg("m-issue-history", "导出点了没反应")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	history, err := h.repo.RecentChat(context.Background(), "group-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || !strings.Contains(history[len(history)-1].Body, "已建单 #1") {
		t.Fatalf("history should include issue number: %#v", history)
	}
}

func TestWorkerStatusQuestionRequiresIssueContext(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"请补充具体问题。"}`

	if err := h.s.HandleMessage(context.Background(), msg("m-no-context", "worker 现在什么状态？")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	sent := strings.Join(h.notifier.all(), "\n")
	if !strings.Contains(sent, "带上 Issue 编号") {
		t.Fatalf("reply = %q", sent)
	}
}

func TestWorkerControlPauseResumeStop(t *testing.T) {
	h := newHarness(t, Config{})
	_, _ = h.repo.CreateWorker(context.Background(), &Worker{
		ProjectID: "prj-1", Issue: 210, State: StateCoding, ChatID: "group-1", Branch: "agent/issue-210",
	})

	if err := h.s.HandleMessage(context.Background(), msg("m-pause", "暂停 #210")); err != nil {
		t.Fatalf("pause: %v", err)
	}
	w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 210)
	if w == nil || w.State != StatePaused {
		t.Fatalf("pause state = %+v", w)
	}

	if err := h.s.HandleMessage(context.Background(), msg("m-resume", "继续 #210")); err != nil {
		t.Fatalf("resume: %v", err)
	}
	w, _ = h.repo.ActiveWorker(context.Background(), "prj-1", 210)
	if w == nil || w.State != StateQueued {
		t.Fatalf("resume state = %+v", w)
	}

	if err := h.s.HandleMessage(context.Background(), msg("m-stop", "停止 #210")); err != nil {
		t.Fatalf("stop: %v", err)
	}
	w, _ = h.repo.ActiveWorker(context.Background(), "prj-1", 210)
	if w != nil {
		t.Fatalf("stopped worker should not be active: %+v", w)
	}
	sent := strings.Join(h.notifier.all(), "\n")
	for _, want := range []string{"已暂停 Issue #210", "已恢复 Issue #210", "已停止 Issue #210"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("missing %q in %q", want, sent)
		}
	}
}

func TestWorkerControlFixQueuesWorker(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.s.HandleMessage(context.Background(), msg("m-fix", "重新派发 #210")); err != nil {
		t.Fatalf("fix: %v", err)
	}
	w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 210)
	if w == nil || w.State != StateQueued {
		t.Fatalf("fix should queue worker: %+v", w)
	}
	if !strings.Contains(strings.Join(h.notifier.all(), "\n"), "已为 Issue #210 重新排队") {
		t.Fatalf("reply = %#v", h.notifier.all())
	}
}

func TestWorkerControlCommandRequiresIssue(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.s.HandleMessage(context.Background(), msg("m-pause-no-issue", "/pause")); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !strings.Contains(strings.Join(h.notifier.all(), "\n"), "请在命令里带上 Issue 编号") {
		t.Fatalf("reply = %#v", h.notifier.all())
	}
	if h.runner.count("chat") != 0 {
		t.Fatalf("explicit command should not call chat worker")
	}
}

func TestWorkerControlDispatchPhraseQueuesWorker(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.s.HandleMessage(context.Background(), msg("m-dispatch", "派发 worker 去修复 #210")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 210)
	if w == nil || w.State != StateQueued {
		t.Fatalf("dispatch should queue worker: %+v", w)
	}
}

func TestWorkerStatusShowsFailedWorkerAndRetry(t *testing.T) {
	h := newHarness(t, Config{})
	_, _ = h.repo.CreateWorker(context.Background(), &Worker{
		ProjectID: "prj-1", Issue: 210, State: StateFailed, ChatID: "group-1", LastError: "build failed",
	})

	if err := h.s.HandleMessage(context.Background(), msg("m-failed-status", "有派发worker去修复这个问题吗 #210")); err != nil {
		t.Fatalf("status: %v", err)
	}
	sent := strings.Join(h.notifier.all(), "\n")
	if !strings.Contains(sent, "执行失败") || !strings.Contains(sent, "build failed") {
		t.Fatalf("failed status reply = %q", sent)
	}

	if err := h.s.HandleMessage(context.Background(), msg("m-retry-failed", "/retry #210")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 210)
	if w == nil || w.State != StateQueued {
		t.Fatalf("retry should queue worker: %+v", w)
	}
	if !strings.Contains(strings.Join(h.notifier.all(), "\n"), "已重试 Issue #210") {
		t.Fatalf("retry reply = %#v", h.notifier.all())
	}
}
