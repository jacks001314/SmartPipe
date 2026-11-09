package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"smartpipe/backend/internal/bot"
	"smartpipe/backend/internal/codexrunner"
	"smartpipe/sdks/lanxin"
)

// inProcessTransport 把请求直接交给 iris app，不监听真实端口（沙箱/CI 可能禁止 listen）。
type inProcessTransport struct{ handler http.Handler }

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// e2eSender 只记录回复，避免测试依赖蓝信网关。
type e2eSender struct {
	mu     sync.Mutex
	groups []string
}

func (s *e2eSender) SendP2PMessage(ctx context.Context, userID string, m *lanxin.Message) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

func (s *e2eSender) SendGroupMessage(ctx context.Context, groupID string, m *lanxin.Message) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups = append(s.groups, groupID)
	return json.RawMessage("{}"), nil
}

type e2eMedia struct{ data []byte }

func (m e2eMedia) DownloadMedia(ctx context.Context, mediaID string) ([]byte, error) {
	return m.data, nil
}

// TestFileMessageAttachmentEndToEnd 覆盖蓝信 file 消息 → bot 下载 → 调度器 HTTP →
// chat worker 提示词的完整链路：模型必须拿到文件名/路径，且文件内容可读。
func TestFileMessageAttachmentEndToEnd(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"配置文件里的 port 是 8080。"}`

	// 临时附件在本轮结束后会被清理，所以在 runner 启动时读取内容。
	var mu sync.Mutex
	read := map[string]string{}
	var readErr []string
	h.runner.before = func(spec codexrunner.Spec) {
		mu.Lock()
		defer mu.Unlock()
		for _, line := range strings.Split(spec.Prompt, "\n") {
			// 附件清单格式为「- 文件 name（mime）：/abs/path」，按全角冒号切出路径。
			sep := strings.LastIndex(line, "：")
			if sep < 0 || !strings.Contains(line[sep:], "smartpipe-chat-attach") {
				continue
			}
			path := strings.TrimSpace(line[sep+len("："):])
			if path == "" {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				readErr = append(readErr, path+": "+err.Error())
				continue
			}
			read[path] = string(raw)
		}
	}

	app := NewServer(ServerConfig{Token: "tok"}, h.s)
	if err := app.Build(); err != nil {
		t.Fatalf("build app: %v", err)
	}
	dispatcher := bot.SchedulerDispatcher{
		BaseURL: "http://scheduler.test", Token: "tok",
		Client: &http.Client{Transport: inProcessTransport{handler: app}},
	}
	handler := bot.NewHandler(bot.Config{BotID: "bot-1", BotName: "pipe", RequireAtMe: true}, &e2eSender{}, dispatcher).
		WithMedia(e2eMedia{data: []byte("port = 8080\n")})

	ev := lanxin.Event{Type: lanxin.EventGroupMessage, Data: json.RawMessage(`{
		"msgType":"file","chatType":"group","from":"u-1","senderName":"张三",
		"msgId":"e2e-file-1","groupId":"group-1","groupName":"研发群",
		"msgData":{"file":{"name":"config.toml","mediaIds":["media-file-1"]}},
		"reminder":{"isAtMe":true}}`)}
	if err := handler.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	waitFor(t, "chat worker with attachment", func() bool { return h.runner.count("chat") == 1 })

	spec := h.runner.last()
	// 真实场景里文件消息常常没有文字说明，调度器不能把它丢掉。
	if !strings.Contains(spec.Prompt, "只带了附件") {
		t.Fatalf("attachment-only message not described in prompt:\n%s", spec.Prompt)
	}
	if !strings.Contains(spec.Prompt, "config.toml") || !strings.Contains(spec.Prompt, "附件") {
		t.Fatalf("file attachment missing from prompt:\n%s", spec.Prompt)
	}
	if len(spec.Images) != 0 {
		t.Fatalf("text file must not be sent as image: %v", spec.Images)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(read) != 1 || len(readErr) != 0 {
		t.Fatalf("chat worker did not get a readable attachment, prompt:\n%s\nerrs=%v", spec.Prompt, readErr)
	}
	for path, content := range read {
		if content != "port = 8080\n" {
			t.Fatalf("attachment content = %q (path %s)", content, path)
		}
	}
}
