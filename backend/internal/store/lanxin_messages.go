package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 蓝信收件箱状态。先记录入站事实，再按处理结果更新，避免消息在过滤/建单失败时消失。
const (
	LanxinStatusReceived     = "received"
	LanxinStatusFiltered     = "filtered"
	LanxinStatusUnbound      = "unbound"
	LanxinStatusIssueCreated = "issue_created"
	LanxinStatusFailed       = "failed"
)

// LanxinMessage 是一条蓝信入站消息的收件箱记录。
type LanxinMessage struct {
	ID                int64     `json:"id"`
	PlatformMessageID string    `json:"platform_message_id"`
	BotID             string    `json:"bot_id"`
	BotName           string    `json:"bot_name"`
	ChatType          string    `json:"chat_type"`
	ChatID            string    `json:"chat_id"`
	UserID            string    `json:"user_id"`
	SenderName        string    `json:"sender_name"`
	GroupName         string    `json:"group_name"`
	MsgType           string    `json:"msg_type"`
	Text              string    `json:"text"`
	IsAtMe            bool      `json:"is_at_me"`
	ProjectID         *string   `json:"project_id"`
	Status            string    `json:"status"`
	Reason            string    `json:"reason"`
	IssueNumber       int       `json:"issue_number"`
	IssueURL          string    `json:"issue_url"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

const lanxinMessageColumns = `id, platform_message_id, bot_id, bot_name, chat_type, chat_id, user_id, sender_name,
    group_name, msg_type, text, is_at_me, project_id, status, reason, issue_number, issue_url,
    created_at, updated_at`

func scanLanxinMessage(row interface{ Scan(...any) error }) (*LanxinMessage, error) {
	var (
		m       LanxinMessage
		project sql.NullString
	)
	err := row.Scan(&m.ID, &m.PlatformMessageID, &m.BotID, &m.BotName, &m.ChatType, &m.ChatID, &m.UserID, &m.SenderName,
		&m.GroupName, &m.MsgType, &m.Text, &m.IsAtMe, &project, &m.Status, &m.Reason,
		&m.IssueNumber, &m.IssueURL, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if project.Valid {
		v := project.String
		m.ProjectID = &v
	}
	return &m, nil
}

// RecordLanxinInbound 写入一条入站消息；同一 platform_message_id 重复上报时只刷新描述字段，
// 不把已经落地的 issue_created 状态降级。
func (s *Store) RecordLanxinInbound(ctx context.Context, m LanxinMessage) (*LanxinMessage, error) {
	if m.PlatformMessageID == "" {
		return nil, fmt.Errorf("store: lanxin message id is required")
	}
	if m.Status == "" {
		m.Status = LanxinStatusReceived
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO lanxin_messages
    (platform_message_id, bot_id, bot_name, chat_type, chat_id, user_id, sender_name, group_name, msg_type, text, is_at_me, project_id, status, reason)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (platform_message_id) DO UPDATE SET
    bot_id      = COALESCE(NULLIF(EXCLUDED.bot_id, ''), lanxin_messages.bot_id),
    bot_name    = COALESCE(NULLIF(EXCLUDED.bot_name, ''), lanxin_messages.bot_name),
    chat_type   = EXCLUDED.chat_type,
    chat_id     = EXCLUDED.chat_id,
    user_id     = EXCLUDED.user_id,
    sender_name = EXCLUDED.sender_name,
    group_name  = EXCLUDED.group_name,
    msg_type    = EXCLUDED.msg_type,
    text        = EXCLUDED.text,
    is_at_me    = EXCLUDED.is_at_me,
    project_id  = COALESCE(EXCLUDED.project_id, lanxin_messages.project_id),
    updated_at  = now()
RETURNING `+lanxinMessageColumns,
		m.PlatformMessageID, m.BotID, m.BotName, m.ChatType, m.ChatID, m.UserID, m.SenderName,
		m.GroupName, m.MsgType, m.Text, m.IsAtMe, m.ProjectID, m.Status, m.Reason)
	out, err := scanLanxinMessage(row)
	if err != nil {
		return nil, fmt.Errorf("store: record lanxin message: %w", err)
	}
	return out, nil
}

// MarkLanxinMessage 按消息 ID 更新最终处理结果；记录不存在时补一条最小记录，
// 保证旧版 bot 只调用 /bridge/messages 时也能在收件箱看到。
func (s *Store) MarkLanxinMessage(ctx context.Context, platformMessageID, status, reason string, issueNumber int, issueURL string) error {
	if platformMessageID == "" {
		return fmt.Errorf("store: lanxin message id is required")
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE lanxin_messages
   SET status = $2, reason = $3, issue_number = $4, issue_url = $5, updated_at = now()
 WHERE platform_message_id = $1`, platformMessageID, status, reason, issueNumber, issueURL)
	if err != nil {
		return fmt.Errorf("store: mark lanxin message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO lanxin_messages
    (platform_message_id, status, reason, issue_number, issue_url)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (platform_message_id) DO NOTHING`,
		platformMessageID, status, reason, issueNumber, issueURL)
	if err != nil {
		return fmt.Errorf("store: mark lanxin message (insert): %w", err)
	}
	return nil
}

// ListLanxinMessages 返回最近的收件箱消息。
func (s *Store) ListLanxinMessagesByBot(ctx context.Context, botID string, limit int) ([]LanxinMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+lanxinMessageColumns+`
FROM lanxin_messages WHERE bot_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, botID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list lanxin bot messages: %w", err)
	}
	defer rows.Close()
	out := make([]LanxinMessage, 0, limit)
	for rows.Next() {
		item, err := scanLanxinMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) ListLanxinMessages(ctx context.Context, limit int) ([]LanxinMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+lanxinMessageColumns+`
FROM lanxin_messages ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list lanxin messages: %w", err)
	}
	defer rows.Close()
	out := make([]LanxinMessage, 0, limit)
	for rows.Next() {
		item, err := scanLanxinMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan lanxin message: %w", err)
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}
