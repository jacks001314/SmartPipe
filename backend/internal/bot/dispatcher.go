package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"smartpipe/sdks/lanxin"
)

// Inbound 是归一化后的入站消息（与平台解耦，Orchestrator 只认这个结构）。
type Inbound struct {
	Platform          string `json:"platform"`           // 固定 "lanxin"
	PlatformMessageID string `json:"platformMessageId"`  // msgId，幂等键
	ChatType          string `json:"chatType"`           // "p2p" | "group"
	SenderID          string `json:"senderId"`           // 蓝信 staffId（用户标识）
	SenderName        string `json:"senderName"`
	GroupID           string `json:"groupId,omitempty"`
	ConversationID    string `json:"conversationId,omitempty"`
	MsgType           string `json:"msgType"`            // 原始 msgType
	Text              string `json:"text"`               // 归一化文本（text/format 均提取）
	IsAtMe            bool   `json:"isAtMe"`

	// Reply 回复该消息的便捷函数（由 Handler 注入；不参与 JSON 序列化）。
	Reply func(ctx context.Context, text string) error `json:"-"`
}

// Dispatcher 把归一化消息交给编排层（M1 先用本地实现占位）。
type Dispatcher interface {
	Dispatch(ctx context.Context, msg Inbound) error
}

// AckDispatcher 占位实现：回复"已收到"回执，等 Orchestrator 接入后替换。
type AckDispatcher struct {
	Snd Sender
}

func (d AckDispatcher) Dispatch(ctx context.Context, msg Inbound) error {
	log.Printf("[bot] dispatch(chat=%s from=%s): %s", msg.ChatType, msg.SenderID, truncate(msg.Text, 80))
	if msg.Reply != nil {
		return msg.Reply(ctx, "已收到，管道处理中。Issue 归档功能将在 Orchestrator 接入后生效。")
	}
	return nil
}

// HTTPDispatcher 把消息转发给 Orchestrator（ORCHESTRATOR_URL 配置后启用）。
type HTTPDispatcher struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (d *HTTPDispatcher) Dispatch(ctx context.Context, msg Inbound) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	url := strings.TrimRight(d.BaseURL, "/") + "/internal/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.Token != "" {
		req.Header.Set("X-Internal-Token", d.Token)
	}
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bot: forward to orchestrator: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("bot: orchestrator returned %d", resp.StatusCode)
	}
	return nil
}

func NewDispatcher(cfg Config, snd Sender) Dispatcher {
	if cfg.OrchestratorURL != "" {
		return &HTTPDispatcher{BaseURL: cfg.OrchestratorURL, Token: cfg.InternalToken}
	}
	return AckDispatcher{Snd: snd}
}

// Sender 由 lanxin.Client 满足；抽接口便于测试。
type Sender interface {
	SendP2PMessage(ctx context.Context, userID string, msg *lanxin.Message) (json.RawMessage, error)
	SendGroupMessage(ctx context.Context, groupID string, msg *lanxin.Message) (json.RawMessage, error)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
