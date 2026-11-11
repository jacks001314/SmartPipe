package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"smartpipe/backend/internal/crash"
	"smartpipe/sdks/lanxin"
)

// MediaFetcher 下载蓝信媒体文件；*lanxin.Client 满足该接口。
type MediaFetcher interface {
	DownloadMedia(ctx context.Context, mediaID string) ([]byte, error)
}

// CrashIntake 是"蓝信一键上报"：下载附件 → 预检 → 转交崩溃上报接口 → 回执 Issue。
type CrashIntake struct {
	IngestURL string
	Token     string
	ProjectID string
	MaxBytes  int
	Media     MediaFetcher
	HTTP      *http.Client
}

// CrashResult 是上报接口返回的可展示结果。
type CrashResult struct {
	Status      string `json:"status"`
	DraftID     string `json:"draft_id,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	IssueNumber int    `json:"issue_number"`
	IssueURL    string `json:"issue_url"`
	Occurrences int    `json:"occurrences"`
	Reason      string `json:"reason"`
}

const defaultCrashMaxBytes = 256 * 1024

func (c *CrashIntake) maxBytes() int {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return defaultCrashMaxBytes
}

// CrashAttachment 是一条崩溃上报附件的完整来源，用于把建单草稿发回原群。
type CrashAttachment struct {
	MediaType  lanxin.MediaType
	MediaID    string
	ChatID     string
	MessageID  string
	BotID      string
	BotName    string
	GroupName  string
	SenderID   string
	SenderName string
}

// HandleAttachment 兼容旧调用：只带聊天目标，不含完整消息元数据。
func (c *CrashIntake) HandleAttachment(ctx context.Context, mediaType lanxin.MediaType, mediaID, chatID string) (*CrashResult, string, error) {
	return c.Handle(ctx, CrashAttachment{MediaType: mediaType, MediaID: mediaID, ChatID: chatID})
}

// Handle 处理一条带附件的消息：预检失败只回执，成功时只生成待确认草稿。
func (c *CrashIntake) Handle(ctx context.Context, in CrashAttachment) (*CrashResult, string, error) {
	mediaType, mediaID := in.MediaType, in.MediaID
	if mediaType != lanxin.MediaFile && mediaType != lanxin.MediaImage {
		return nil, fmt.Sprintf("只支持文件或图片形式的 crash_bundle（当前类型 %d）", mediaType), nil
	}
	if c.Media == nil {
		return nil, "机器人未配置媒体下载能力", fmt.Errorf("bot: media fetcher missing")
	}
	raw, err := c.Media.DownloadMedia(ctx, mediaID)
	if err != nil {
		return nil, "附件下载失败，请重试或改用文本反馈", err
	}
	if len(raw) == 0 {
		return nil, "附件是空文件，请检查后重新发送", nil
	}
	if len(raw) > c.maxBytes() {
		return nil, fmt.Sprintf("附件 %d 字节，超过上限 %d 字节（%d KB）。请只保留异常与调用栈，去掉日志和截图。",
			len(raw), c.maxBytes(), c.maxBytes()/1024), nil
	}
	var bundle crash.Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, "附件不是合法的 crash_bundle JSON：" + err.Error(), nil
	}
	if strings.TrimSpace(bundle.App) == "" || strings.TrimSpace(bundle.Exception.Type) == "" {
		return nil, "crash_bundle 缺少 app 或 exception.type 字段", nil
	}
	res, err := c.ingest(ctx, raw, in)
	if err != nil {
		return nil, "上报失败：" + err.Error(), err
	}
	return res, summarize(res), nil
}

func (c *CrashIntake) ingest(ctx context.Context, raw []byte, in CrashAttachment) (*CrashResult, error) {
	payload, err := json.Marshal(map[string]any{
		"project_id":  c.ProjectID,
		"chat_id":     in.ChatID,
		"message_id":  in.MessageID,
		"bot_id":      in.BotID,
		"bot_name":    in.BotName,
		"group_name":  in.GroupName,
		"sender_id":   in.SenderID,
		"sender_name": in.SenderName,
		"source":      "lanxin",
		"bundle":      json.RawMessage(raw),
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.IngestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("X-Ingest-Token", c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 {
		log.Printf("[bot] crash ingest HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 429/422 也带 data.status（rate_limited / rejected），照常解析成可展示结果。
	var out struct {
		Data CrashResult `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Data.Status == "" {
		log.Printf("[bot] crash ingest HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return &out.Data, nil
}

func summarize(res *CrashResult) string {
	if res == nil {
		return "上报完成，但没有返回结果"
	}
	switch res.Status {
	case "awaiting_confirmation":
		if strings.TrimSpace(res.Prompt) != "" {
			return res.Prompt
		}
		return "崩溃已解析，等待群里确认建单。"
	case "created":
		return fmt.Sprintf("已建单：%s（Issue #%d），lead 会跟进。", res.IssueURL, res.IssueNumber)
	case "deduped":
		return fmt.Sprintf("这个崩溃已在 24 小时内上报过，已合并为第 %d 次出现（Issue #%d）。", res.Occurrences, res.IssueNumber)
	case "rate_limited":
		return "上报过于频繁，已进入限流；请稍后再试或直接在群里描述问题。"
	default:
		if res.Reason != "" {
			return "未建单：" + res.Reason
		}
		return "上报已受理（状态 " + res.Status + "）。"
	}
}
