package bot

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

// InboxReport 是 bot 上报给 bridge 的入站消息；用于记录未进入建单流程的消息。
type InboxReport struct {
	MessageID  string
	ChatType   string
	ChatID     string
	UserID     string
	SenderName string
	GroupName  string
	MsgType    string
	Text       string
	IsAtMe     bool
	Reason     string
}

// InboxReporter 把收件箱记录送到持久层（bridge）。
type InboxReporter interface {
	Report(ctx context.Context, r InboxReport) error
}

// BridgeInbox 通过 bridge 的 /bridge/inbound 记录消息。
type BridgeInbox struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (b BridgeInbox) Report(ctx context.Context, r InboxReport) error {
	if strings.TrimSpace(b.BaseURL) == "" {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"message_id":  r.MessageID,
		"chat_type":   r.ChatType,
		"chat_id":     r.ChatID,
		"user_id":     r.UserID,
		"sender_name": r.SenderName,
		"group_name":  r.GroupName,
		"msg_type":    r.MsgType,
		"text":        r.Text,
		"is_at_me":    r.IsAtMe,
		"reason":      r.Reason,
	})
	if err != nil {
		return err
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/bridge/inbound", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.Token != "" {
		req.Header.Set("X-Bridge-Token", b.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("bot: inbox report http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
