// Package issuedraft 定义所有自动建单入口共用的待确认草稿。
// 草稿先落库并回群，只有群成员明确确认后才允许创建 GitHub Issue。
package issuedraft

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// TTL 是草稿有效期：过期后必须重新描述问题。
const TTL = 30 * time.Minute

const (
	StatusPending    = "pending"
	StatusCreating   = "creating"
	StatusCreated    = "created"
	StatusCancelled  = "cancelled"
	StatusSuperseded = "superseded"
	StatusExpired    = "expired"
)

// Kind 区分草稿来源，确认时据此选择正文、标签和后续状态回写。
const (
	KindChat   = "chat"
	KindCrash  = "crash"
	KindBridge = "bridge"
)

// ErrNotFound 表示草稿不存在。
var ErrNotFound = errors.New("issuedraft: not found")

// IDPattern 匹配群消息里的草稿编号。
var IDPattern = regexp.MustCompile(`(?i)draft-[a-f0-9]{8,16}`)

// Draft 是等待群里确认的建单草稿。
type Draft struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	ProjectID       string    `json:"project_id"`
	ChatID          string    `json:"chat_id"`
	SourceMessageID string    `json:"source_message_id"`
	BotID           string    `json:"bot_id"`
	BotName         string    `json:"bot_name"`
	GroupID         string    `json:"group_id"`
	GroupName       string    `json:"group_name"`
	SenderID        string    `json:"sender_id"`
	SenderName      string    `json:"sender_name"`
	SourceText      string    `json:"source_text"`
	Title           string    `json:"title"`
	Body            string    `json:"body"`
	Severity        string    `json:"severity"`
	RefID           string    `json:"ref_id"`
	Repo            string    `json:"repo"`
	Labels          []string  `json:"labels"`
	Auto            bool      `json:"auto"`
	Status          string    `json:"status"`
	Issue           int       `json:"issue_number"`
	IssueURL        string    `json:"issue_url"`
	ConfirmedByID   string    `json:"confirmed_by_id"`
	ConfirmedByName string    `json:"confirmed_by_name"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// NewID 生成不易与普通文本混淆的草稿编号。
func NewID() (string, error) {
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "DRAFT-" + strings.ToUpper(hex.EncodeToString(raw[:])), nil
}

// Prompt 是发回群里的确认提示。
func Prompt(d *Draft) string {
	if d == nil {
		return ""
	}
	body := clamp(strings.TrimSpace(d.Body), 900)
	return fmt.Sprintf("【待确认建单】%s\n标题：%s\n级别：%s\n摘要：%s\n\n请在本群回复：\n@机器人 确认建单 %s\n@机器人 取消建单 %s\n确认前不会创建 Issue，也不会派发 worker；草稿 30 分钟内有效。",
		d.ID, d.Title, d.Severity, body, d.ID, d.ID)
}

// ParseCommand 只接受消息开头（可带 @ 机器人）的明确命令，避免普通讨论误触发建单。
func ParseCommand(text string) (action, draftID string, ok bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	command := strings.Join(fields, " ")
	switch {
	case strings.HasPrefix(command, "确认建单"):
		action = "confirm"
	case strings.HasPrefix(command, "取消建单"), strings.HasPrefix(command, "撤回建单"), strings.HasPrefix(command, "撤销建单"):
		action = "cancel"
	default:
		return "", "", false
	}
	if id := IDPattern.FindString(command); id != "" {
		draftID = strings.ToUpper(id)
	}
	return action, draftID, true
}

func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
