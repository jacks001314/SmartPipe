package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"smartpipe/backend/internal/bot"
)

var _ BotClient = (*HTTPBotClient)(nil)

// HTTPBotClient 通过 bot 服务的 /internal/reply 发送蓝信消息。
// 蓝信凭证只存在 bot 进程内，编排层拿不到（最小授权）。
type HTTPBotClient struct {
	BaseURL string
	Token   string
	HC      *http.Client
}

func (c *HTTPBotClient) Reply(ctx context.Context, chatType, targetID, refMsgID, text string) error {
	body := bot.ReplyRequest{ChatType: chatType, TargetID: targetID, RefMsgID: refMsgID, Text: text}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/internal/reply", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("X-Internal-Token", c.Token)
	}
	hc := c.HC
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("bot reply: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("bot reply: http %d", resp.StatusCode)
	}
	return nil
}
