package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"smartpipe/backend/internal/bot"
)

func postJSON(t *testing.T, url, token string, payload any) int {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Scheduler-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestHTTPEndToEnd 走真实 HTTP 链路：蓝信 bot 的 dispatcher → 调度器 API → 建单 →
// fix worker → PR → 人工评论续轮 → Issue 关闭收尾。
func TestHTTPEndToEnd(t *testing.T) {
	h := newHarness(t, Config{})
	app := NewServer(ServerConfig{Token: "tok"}, h.s)
	if err := app.Build(); err != nil {
		t.Fatalf("build app: %v", err)
	}
	ts := httptest.NewServer(app)
	defer ts.Close()

	// 无令牌必须被拒绝。
	if code := postJSON(t, ts.URL+"/internal/scheduler/messages", "", map[string]string{"text": "hi"}); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}

	h.runner.chatReply = `{"action":"issue","title":"登录超时","body":"偶发 30 秒超时","severity":"high"}`
	dispatcher := bot.SchedulerDispatcher{BaseURL: ts.URL, Token: "tok"}
	err := dispatcher.Dispatch(context.Background(), bot.Inbound{
		Platform: "lanxin", PlatformMessageID: "e2e-1", ChatType: "group",
		GroupID: "group-1", GroupName: "研发群", SenderID: "u1", SenderName: "张三",
		Text: "登录老是超时", BotID: "bot-1", IsAtMe: true,
	})
	if err != nil {
		t.Fatalf("bot dispatch: %v", err)
	}
	waitFor(t, "issue + queued worker", func() bool {
		w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 1)
		return w != nil
	})
	if h.tracker.issueCount() != 1 || h.tracker.issueAt(0).Title != "登录超时" {
		t.Fatalf("issue not created: %#v", h.tracker.issues)
	}

	h.runner.fixReply = "修复了超时时间设置。"
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 1)
	if w.State != StateAwaitingReview || h.tracker.prCount() != 1 {
		t.Fatalf("want PR and awaiting_review, got state=%s prs=%d", w.State, h.tracker.prCount())
	}

	// 人工在 Issue 上评论：事件经 HTTP 回流，触发同一 worker 再改一轮。
	if code := postJSON(t, ts.URL+"/internal/scheduler/github", "tok", IssueEvent{
		Event: "issue_comment", Action: "created", ProjectID: "prj-1", Repo: "acme/core",
		Issue: 1, Author: "lisi", Body: "请把超时改成可配置",
	}); code != http.StatusAccepted {
		t.Fatalf("comment event code=%d", code)
	}
	waitFor(t, "resume turn", func() bool { return h.runner.count("fix") == 2 })
	h.s.Wait()
	if !bytes.Contains([]byte(h.runner.last().Prompt), []byte("可配置")) {
		t.Fatalf("trigger missing in prompt: %s", h.runner.last().Prompt)
	}

	// Issue 关闭：worker 收尾，worktree 回收。
	if code := postJSON(t, ts.URL+"/internal/scheduler/github", "tok", IssueEvent{
		Event: "issues", Action: "closed", ProjectID: "prj-1", Repo: "acme/core", Issue: 1,
	}); code != http.StatusAccepted {
		t.Fatalf("close event code=%d", code)
	}
	waitFor(t, "worker closed", func() bool {
		w, _ := h.repo.GetWorker(context.Background(), w.ID)
		return w != nil && w.State == StateClosed
	})
	if h.ws.cleanedCount() != 1 {
		t.Fatalf("worktree should be cleaned once, got %d", h.ws.cleanedCount())
	}
}
