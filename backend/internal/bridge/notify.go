package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Notifier 把建单结果回执到来源会话（蓝信）。
type Notifier interface {
	Reply(ctx context.Context, botID, chatType, targetID, refMsgID, text string) error
}

// BotNotifier 调用 bot 的 /internal/reply 内网接口。
type BotNotifier struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewBotNotifier 构造回执客户端；baseURL 形如 http://127.0.0.1:8091。
func NewBotNotifier(baseURL, token string) *BotNotifier {
	return &BotNotifier{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Reply 发送一条文本回执。
func (n *BotNotifier) Reply(ctx context.Context, botID, chatType, targetID, refMsgID, text string) error {
	if strings.TrimSpace(targetID) == "" {
		return fmt.Errorf("bridge: reply target is empty")
	}
	payload := map[string]string{"botId": botID, "chatType": chatType, "targetId": targetID, "text": text}
	if refMsgID != "" {
		payload["refMsgId"] = refMsgID
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.BaseURL+"/internal/reply", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.Token != "" {
		req.Header.Set("X-Internal-Token", n.Token)
	}
	client := n.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bridge: bot reply: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("bridge: bot reply HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

// BuildReceipt 生成回给反馈人的文本。
func BuildReceipt(issueNumber int, issueURL string) string {
	return fmt.Sprintf("已收到反馈，已创建 GitHub Issue #%d：%s\n处理进展会同步到该 Issue。", issueNumber, issueURL)
}
