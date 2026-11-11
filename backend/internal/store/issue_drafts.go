package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"smartpipe/backend/internal/issuedraft"
)

const issueDraftColumns = `id, kind, project_id, chat_id, source_message_id, bot_id, bot_name,
    group_id, group_name, sender_id, sender_name, source_text, title, body, severity, ref_id, repo, labels, auto,
    status, issue_number, issue_url, confirmed_by_id, confirmed_by_name, created_at, updated_at, expires_at`

func scanIssueDraft(row interface{ Scan(...any) error }) (*issuedraft.Draft, error) {
	var (
		d      issuedraft.Draft
		labels string
	)
	err := row.Scan(&d.ID, &d.Kind, &d.ProjectID, &d.ChatID, &d.SourceMessageID, &d.BotID, &d.BotName,
		&d.GroupID, &d.GroupName, &d.SenderID, &d.SenderName, &d.SourceText, &d.Title, &d.Body,
		&d.Severity, &d.RefID, &d.Repo, &labels, &d.Auto, &d.Status, &d.Issue, &d.IssueURL,
		&d.ConfirmedByID, &d.ConfirmedByName, &d.CreatedAt, &d.UpdatedAt, &d.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if labels != "" {
		_ = json.Unmarshal([]byte(labels), &d.Labels)
	}
	return &d, nil
}

// CreateIssueDraft 写入待确认草稿；同一来源消息只允许一份，同一会话的新草稿会取代旧草稿。
func (s *Store) CreateIssueDraft(ctx context.Context, d *issuedraft.Draft) (bool, error) {
	now := time.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.ExpiresAt.IsZero() {
		d.ExpiresAt = now.Add(issuedraft.TTL)
	}
	if d.Kind == "" {
		d.Kind = issuedraft.KindChat
	}
	if d.Status == "" {
		d.Status = issuedraft.StatusPending
	}
	labels, _ := json.Marshal(d.Labels)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: issue draft tx: %w", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `
INSERT INTO scheduler_issue_drafts
 (id, kind, project_id, chat_id, source_message_id, bot_id, bot_name, group_id, group_name,
  sender_id, sender_name, source_text, title, body, severity, ref_id, repo, labels, auto,
  status, created_at, updated_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
ON CONFLICT (source_message_id) WHERE source_message_id <> '' DO NOTHING
RETURNING `+issueDraftColumns,
		d.ID, d.Kind, d.ProjectID, d.ChatID, d.SourceMessageID, d.BotID, d.BotName, d.GroupID, d.GroupName,
		d.SenderID, d.SenderName, d.SourceText, d.Title, d.Body, d.Severity, d.RefID, d.Repo, string(labels),
		d.Auto, d.Status, d.CreatedAt, d.UpdatedAt, d.ExpiresAt)
	got, err := scanIssueDraft(row)
	if err != nil {
		return false, fmt.Errorf("store: create issue draft: %w", err)
	}
	if got == nil {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$4, updated_at=$5
WHERE project_id=$1 AND chat_id=$2 AND status=$3 AND id<>$6`,
		d.ProjectID, d.ChatID, issuedraft.StatusPending, issuedraft.StatusSuperseded, now, d.ID); err != nil {
		return false, fmt.Errorf("store: supersede issue drafts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: commit issue draft: %w", err)
	}
	*d = *got
	return true, nil
}

// IssueDraftBySource 按来源消息读取草稿（含已处理状态），用于入站消息幂等。
func (s *Store) IssueDraftBySource(ctx context.Context, sourceMessageID string) (*issuedraft.Draft, error) {
	if strings.TrimSpace(sourceMessageID) == "" {
		return nil, nil
	}
	got, err := scanIssueDraft(s.db.QueryRowContext(ctx, `SELECT `+issueDraftColumns+`
FROM scheduler_issue_drafts WHERE source_message_id=$1`, sourceMessageID))
	if err != nil {
		return nil, fmt.Errorf("store: issue draft by source: %w", err)
	}
	return got, nil
}

// LatestPendingIssueDraft 返回会话中最近一份未过期草稿，并把过期草稿标记为 expired。
func (s *Store) LatestPendingIssueDraft(ctx context.Context, projectID, chatID string, now time.Time) (*issuedraft.Draft, error) {
	_, _ = s.db.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$4, updated_at=$3 WHERE project_id=$1 AND chat_id=$2 AND status=$5 AND expires_at <= $3`,
		projectID, chatID, now, issuedraft.StatusExpired, issuedraft.StatusPending)
	got, err := scanIssueDraft(s.db.QueryRowContext(ctx, `SELECT `+issueDraftColumns+`
FROM scheduler_issue_drafts
WHERE project_id=$1 AND chat_id=$2 AND status=$3 AND expires_at>$4
ORDER BY created_at DESC LIMIT 1`, projectID, chatID, issuedraft.StatusPending, now))
	if err != nil {
		return nil, fmt.Errorf("store: latest issue draft: %w", err)
	}
	return got, nil
}

// ClaimIssueDraft 原子领取草稿，避免多人同时确认造成重复建单。
func (s *Store) ClaimIssueDraft(ctx context.Context, id, projectID, chatID, actorID, actorName string, now time.Time) (*issuedraft.Draft, error) {
	got, err := scanIssueDraft(s.db.QueryRowContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$6, confirmed_by_id=$7, confirmed_by_name=$8, updated_at=$9
WHERE id=$1 AND project_id=$2 AND chat_id=$3 AND status=$4 AND expires_at>$5
RETURNING `+issueDraftColumns,
		id, projectID, chatID, issuedraft.StatusPending, now, issuedraft.StatusCreating, actorID, actorName, now))
	if err != nil {
		return nil, fmt.Errorf("store: claim issue draft: %w", err)
	}
	return got, nil
}

// FinishIssueDraft 记录草稿的最终状态。
func (s *Store) FinishIssueDraft(ctx context.Context, id, status string, issue int, issueURL string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$2, issue_number=$3, issue_url=$4, updated_at=$5 WHERE id=$1`, id, status, issue, issueURL, now)
	if err != nil {
		return fmt.Errorf("store: finish issue draft: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return issuedraft.ErrNotFound
	}
	return nil
}

// ReleaseIssueDraft 在建单失败后把草稿退回 pending，允许再次确认。
func (s *Store) ReleaseIssueDraft(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$3, updated_at=$2 WHERE id=$1 AND status=$4`, id, now, issuedraft.StatusPending, issuedraft.StatusCreating)
	if err != nil {
		return fmt.Errorf("store: release issue draft: %w", err)
	}
	return nil
}
