package scheduler

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

// HTTPNotifier 通过 bot 的 /internal/reply 发蓝信消息，凭证留在 bot 进程。
type HTTPNotifier struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPNotifier 构造 bot 客户端。
func NewHTTPNotifier(baseURL, token string) *HTTPNotifier {
	return &HTTPNotifier{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// SendGroup 发送群消息。
func (n *HTTPNotifier) SendGroup(ctx context.Context, botID, chatID, replyTo, text string) error {
	payload := map[string]any{
		"botId":    botID,
		"chatType": "group",
		"targetId": chatID,
		"text":     text,
	}
	if replyTo != "" {
		payload["refMsgId"] = replyTo
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.BaseURL+"/internal/reply", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.Token != "" {
		req.Header.Set("X-Internal-Token", n.Token)
	}
	resp, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("scheduler: bot reply %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
