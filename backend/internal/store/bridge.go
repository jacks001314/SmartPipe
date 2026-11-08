package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BridgeLink 记录一条蓝信（或其它渠道）消息与 GitHub Issue 的绑定。
type BridgeLink struct {
	ID              int64     `json:"id"`
	SourceMessageID string    `json:"source_message_id"`
	Channel         string    `json:"channel"`
	ChatID          string    `json:"chat_id"`
	UserID          string    `json:"user_id"`
	ProjectID       *string   `json:"project_id"`
	Repo            string    `json:"repo"`
	IssueNumber     int       `json:"issue_number"`
	IssueURL        string    `json:"issue_url"`
	ReceiptStatus   string    `json:"receipt_status"`
	ReceiptError    string    `json:"receipt_error"`
	CreatedAt       time.Time `json:"created_at"`
}

// ErrDuplicate 表示重复消息或重复 webhook 投递。
var ErrDuplicate = errors.New("store: duplicate")

const bridgeLinkColumns = `id, source_message_id, channel, chat_id, user_id, project_id,
    repo, issue_number, issue_url, receipt_status, receipt_error, created_at`

func scanBridgeLink(row interface{ Scan(...any) error }) (*BridgeLink, error) {
	var (
		l       BridgeLink
		project sql.NullString
	)
	err := row.Scan(&l.ID, &l.SourceMessageID, &l.Channel, &l.ChatID, &l.UserID, &project,
		&l.Repo, &l.IssueNumber, &l.IssueURL,
		&l.ReceiptStatus, &l.ReceiptError, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if project.Valid {
		v := project.String
		l.ProjectID = &v
	}
	return &l, nil
}

// GetBridgeLinkByMessage 按来源消息 ID 查询已有映射。
func (s *Store) GetBridgeLinkByMessage(ctx context.Context, sourceMessageID string) (*BridgeLink, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+bridgeLinkColumns+` FROM bridge_links WHERE source_message_id = $1`, sourceMessageID)
	l, err := scanBridgeLink(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get bridge link: %w", err)
	}
	return l, nil
}

// GetBridgeLink 按主键读取映射。
func (s *Store) GetBridgeLink(ctx context.Context, id int64) (*BridgeLink, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+bridgeLinkColumns+` FROM bridge_links WHERE id = $1`, id)
	l, err := scanBridgeLink(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get bridge link: %w", err)
	}
	return l, nil
}

// CreateBridgeLink 写入映射；同一 source_message_id 重复写入返回 ErrDuplicate。
func (s *Store) CreateBridgeLink(ctx context.Context, l BridgeLink) (*BridgeLink, error) {
	if l.Channel == "" {
		l.Channel = "lanxin"
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO bridge_links (source_message_id, channel, chat_id, user_id, project_id, repo, issue_number, issue_url)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING `+bridgeLinkColumns,
		l.SourceMessageID, l.Channel, l.ChatID, l.UserID, l.ProjectID, l.Repo, l.IssueNumber, l.IssueURL)
	out, err := scanBridgeLink(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, fmt.Errorf("store: create bridge link: %w", err)
	}
	return out, nil
}

// MarkReceipt 记录蓝信回执结果。
func (s *Store) MarkReceipt(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bridge_links SET receipt_status = $2, receipt_error = $3 WHERE id = $1`, id, status, errMsg)
	return err
}

// ListBridgeLinks 返回最近的映射记录。
func (s *Store) ListBridgeLinks(ctx context.Context, limit int) ([]BridgeLink, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+bridgeLinkColumns+` FROM bridge_links ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list bridge links: %w", err)
	}
	defer rows.Close()

	out := make([]BridgeLink, 0, limit)
	for rows.Next() {
		l, err := scanBridgeLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// RecordWebhookDelivery 记录一次 webhook 投递；重复投递返回 ErrDuplicate。
func (s *Store) RecordWebhookDelivery(ctx context.Context, deliveryID, event, repo string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webhook_deliveries (delivery_id, event, repo) VALUES ($1,$2,$3)`, deliveryID, event, repo)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("store: record webhook delivery: %w", err)
	}
	return nil
}

// CountWebhookDeliveries 返回已记录的事件数，运维与测试用。
func (s *Store) CountWebhookDeliveries(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM webhook_deliveries`).Scan(&n)
	return n, err
}
