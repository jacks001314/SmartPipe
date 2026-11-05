package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PatchVerification 是一次补丁验证的归档记录。
type PatchVerification struct {
	ID           int64           `json:"id"`
	ProjectID    *string         `json:"project_id"`
	IssueRef     string          `json:"issue_ref"`
	Repo         string          `json:"repo"`
	BaseCommit   string          `json:"base_commit"`
	HeadCommit   string          `json:"head_commit"`
	PatchHash    string          `json:"patch_hash"`
	OK           bool            `json:"ok"`
	Files        json.RawMessage `json:"files"`
	ChangedLines int             `json:"changed_lines"`
	Checks       json.RawMessage `json:"checks"`
	Error        string          `json:"error"`
	DurationMS   int             `json:"duration_ms"`
	Report       json.RawMessage `json:"report"`
	CreatedAt    time.Time       `json:"created_at"`
}

// CreateVerification 写入一条验证记录。
func (s *Store) CreateVerification(ctx context.Context, v PatchVerification) (*PatchVerification, error) {
	if len(v.Files) == 0 {
		v.Files = json.RawMessage("[]")
	}
	if len(v.Checks) == 0 {
		v.Checks = json.RawMessage("[]")
	}
	if len(v.Report) == 0 {
		v.Report = json.RawMessage("{}")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO patch_verifications
    (project_id, issue_ref, repo, base_commit, head_commit, patch_hash, ok, files, changed_lines, checks, error, duration_ms, report)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
RETURNING id, created_at`,
		v.ProjectID, v.IssueRef, v.Repo, v.BaseCommit, v.HeadCommit, v.PatchHash, v.OK,
		v.Files, v.ChangedLines, v.Checks, v.Error, v.DurationMS, v.Report)
	if err := row.Scan(&v.ID, &v.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: create verification: %w", err)
	}
	return &v, nil
}

// ListVerifications 返回最近的验证记录（不含 report 大字段内容以外的处理）。
func (s *Store) ListVerifications(ctx context.Context, limit int) ([]PatchVerification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, issue_ref, repo, base_commit, head_commit, patch_hash, ok, files,
       changed_lines, checks, error, duration_ms, report, created_at
  FROM patch_verifications ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list verifications: %w", err)
	}
	defer rows.Close()
	out := make([]PatchVerification, 0, limit)
	for rows.Next() {
		var (
			v       PatchVerification
			project sql.NullString
		)
		if err := rows.Scan(&v.ID, &project, &v.IssueRef, &v.Repo, &v.BaseCommit, &v.HeadCommit, &v.PatchHash,
			&v.OK, &v.Files, &v.ChangedLines, &v.Checks, &v.Error, &v.DurationMS, &v.Report, &v.CreatedAt); err != nil {
			return nil, err
		}
		if project.Valid {
			id := project.String
			v.ProjectID = &id
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVerification 按 ID 读取一条记录。
func (s *Store) GetVerification(ctx context.Context, id int64) (*PatchVerification, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, issue_ref, repo, base_commit, head_commit, patch_hash, ok, files,
       changed_lines, checks, error, duration_ms, report, created_at
  FROM patch_verifications WHERE id = $1`, id)
	var (
		v       PatchVerification
		project sql.NullString
	)
	err := row.Scan(&v.ID, &project, &v.IssueRef, &v.Repo, &v.BaseCommit, &v.HeadCommit, &v.PatchHash,
		&v.OK, &v.Files, &v.ChangedLines, &v.Checks, &v.Error, &v.DurationMS, &v.Report, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get verification: %w", err)
	}
	if project.Valid {
		id := project.String
		v.ProjectID = &id
	}
	return &v, nil
}
