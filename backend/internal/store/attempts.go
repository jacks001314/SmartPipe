package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxFixAttempts 是同一 Issue 允许的失败次数，超过后置 blocked。
const MaxFixAttempts = 3

// IssueAttempt 记录一个 Issue 的验证失败次数与状态。
type IssueAttempt struct {
	IssueRef           string    `json:"issue_ref"`
	ProjectID          *string   `json:"project_id"`
	Attempts           int       `json:"attempts"`
	State              string    `json:"state"`
	LastVerificationID *int64    `json:"last_verification_id"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// RecordIssueAttempt 记录一次验证结果：成功置 passed，失败累加并可能在达到上限后置 blocked。
func (s *Store) RecordIssueAttempt(ctx context.Context, issueRef string, projectID *string, verificationID int64, ok bool) (*IssueAttempt, error) {
	if issueRef == "" {
		return nil, errors.New("store: issue_ref is required")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO issue_attempts (issue_ref, project_id, attempts, state, last_verification_id, updated_at)
VALUES ($1, $2,
        CASE WHEN $4 THEN 0 ELSE 1 END,
        CASE WHEN $4 THEN 'passed' WHEN $5 <= 1 THEN 'blocked' ELSE 'retry_allowed' END,
        $3, now())
ON CONFLICT (issue_ref) DO UPDATE SET
    project_id = COALESCE(EXCLUDED.project_id, issue_attempts.project_id),
    attempts = CASE WHEN $4 THEN issue_attempts.attempts ELSE issue_attempts.attempts + 1 END,
    state = CASE
        WHEN $4 THEN 'passed'
        WHEN issue_attempts.attempts + 1 >= $5 THEN 'blocked'
        ELSE 'retry_allowed'
    END,
    last_verification_id = EXCLUDED.last_verification_id,
    updated_at = now()
RETURNING issue_ref, project_id, attempts, state, last_verification_id, updated_at`,
		issueRef, projectID, verificationID, ok, MaxFixAttempts)
	var (
		a       IssueAttempt
		project sql.NullString
		lastID  sql.NullInt64
	)
	if err := row.Scan(&a.IssueRef, &project, &a.Attempts, &a.State, &lastID, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store: record issue attempt: %w", err)
	}
	if project.Valid {
		v := project.String
		a.ProjectID = &v
	}
	if lastID.Valid {
		v := lastID.Int64
		a.LastVerificationID = &v
	}
	return &a, nil
}

// GetIssueAttempt 读取一个 Issue 的尝试状态。
func (s *Store) GetIssueAttempt(ctx context.Context, issueRef string) (*IssueAttempt, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT issue_ref, project_id, attempts, state, last_verification_id, updated_at
  FROM issue_attempts WHERE issue_ref = $1`, issueRef)
	var (
		a       IssueAttempt
		project sql.NullString
		lastID  sql.NullInt64
	)
	err := row.Scan(&a.IssueRef, &project, &a.Attempts, &a.State, &lastID, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get issue attempt: %w", err)
	}
	if project.Valid {
		v := project.String
		a.ProjectID = &v
	}
	if lastID.Valid {
		v := lastID.Int64
		a.LastVerificationID = &v
	}
	return &a, nil
}

// ListIssueAttempts 返回最近的 Issue 尝试状态。
func (s *Store) ListIssueAttempts(ctx context.Context, limit int) ([]IssueAttempt, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT issue_ref, project_id, attempts, state, last_verification_id, updated_at
  FROM issue_attempts ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list issue attempts: %w", err)
	}
	defer rows.Close()
	out := make([]IssueAttempt, 0, limit)
	for rows.Next() {
		var (
			a       IssueAttempt
			project sql.NullString
			lastID  sql.NullInt64
		)
		if err := rows.Scan(&a.IssueRef, &project, &a.Attempts, &a.State, &lastID, &a.UpdatedAt); err != nil {
			return nil, err
		}
		if project.Valid {
			v := project.String
			a.ProjectID = &v
		}
		if lastID.Valid {
			v := lastID.Int64
			a.LastVerificationID = &v
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
