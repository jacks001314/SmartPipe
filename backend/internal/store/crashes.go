package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CrashReport 是一条崩溃上报记录。
type CrashReport struct {
	ID           int64           `json:"id"`
	ProjectID    *string         `json:"project_id"`
	Fingerprint  string          `json:"fingerprint"`
	App          string          `json:"app"`
	Version      string          `json:"version"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	UserID       string          `json:"user_id"`
	DeviceID     string          `json:"device_id"`
	Source       string          `json:"source"`
	Repo         string          `json:"repo"`
	IssueNumber  int             `json:"issue_number"`
	IssueURL     string          `json:"issue_url"`
	Status       string          `json:"status"`
	RejectReason string          `json:"reject_reason"`
	Occurrences  int             `json:"occurrences"`
	SizeBytes    int             `json:"size_bytes"`
	Bundle       json.RawMessage `json:"bundle"`
	OccurredAt   *time.Time      `json:"occurred_at"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

const crashColumns = `id, project_id, fingerprint, app, version, os, arch, user_id, device_id,
source, repo, issue_number, issue_url, status, reject_reason, occurrences, size_bytes, bundle,
occurred_at, created_at, updated_at`

func scanCrash(row interface{ Scan(...any) error }) (*CrashReport, error) {
	var (
		r        CrashReport
		project  sql.NullString
		occurred sql.NullTime
	)
	err := row.Scan(&r.ID, &project, &r.Fingerprint, &r.App, &r.Version, &r.OS, &r.Arch, &r.UserID,
		&r.DeviceID, &r.Source, &r.Repo, &r.IssueNumber, &r.IssueURL, &r.Status, &r.RejectReason,
		&r.Occurrences, &r.SizeBytes, &r.Bundle, &occurred, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if project.Valid {
		v := project.String
		r.ProjectID = &v
	}
	if occurred.Valid {
		t := occurred.Time
		r.OccurredAt = &t
	}
	return &r, nil
}

// CreateCrashReport 写入上报记录。
func (s *Store) CreateCrashReport(ctx context.Context, r CrashReport) (*CrashReport, error) {
	if len(r.Bundle) == 0 {
		r.Bundle = json.RawMessage("{}")
	}
	if r.Occurrences <= 0 {
		r.Occurrences = 1
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO crash_reports (project_id, fingerprint, app, version, os, arch, user_id, device_id,
    source, repo, issue_number, issue_url, status, reject_reason, occurrences, size_bytes, bundle, occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
RETURNING id, created_at, updated_at`,
		r.ProjectID, r.Fingerprint, r.App, r.Version, r.OS, r.Arch, r.UserID, r.DeviceID,
		r.Source, r.Repo, r.IssueNumber, r.IssueURL, r.Status, r.RejectReason, r.Occurrences,
		r.SizeBytes, r.Bundle, r.OccurredAt)
	if err := row.Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store: create crash report: %w", err)
	}
	return &r, nil
}

// FindCrashByFingerprint 查找窗口期内同指纹的最近记录（排除被拒绝的）。
func (s *Store) FindCrashByFingerprint(ctx context.Context, projectID *string, fingerprint string, since time.Time) (*CrashReport, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+crashColumns+` FROM crash_reports
 WHERE project_id IS NOT DISTINCT FROM $1 AND fingerprint = $2 AND status <> 'rejected' AND created_at >= $3
 ORDER BY id DESC LIMIT 1`, projectID, fingerprint, since)
	r, err := scanCrash(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: find crash report: %w", err)
	}
	return r, nil
}

// BumpCrashReport 记录一次重复出现。
func (s *Store) BumpCrashReport(ctx context.Context, id int64, at time.Time) (int, error) {
	var occurrences int
	err := s.db.QueryRowContext(ctx,
		`UPDATE crash_reports SET occurrences = occurrences + 1, updated_at = $2 WHERE id = $1 RETURNING occurrences`,
		id, at).Scan(&occurrences)
	if err != nil {
		return 0, fmt.Errorf("store: bump crash report: %w", err)
	}
	return occurrences, nil
}

// CountCrashesByUser 统计窗口期内同一用户的上报次数。
func (s *Store) CountCrashesByUser(ctx context.Context, projectID *string, userID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crash_reports
 WHERE project_id IS NOT DISTINCT FROM $1 AND user_id = $2 AND created_at >= $3`, projectID, userID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count crash reports: %w", err)
	}
	return n, nil
}

// ListCrashReports 返回最近的崩溃上报。
func (s *Store) ListCrashReports(ctx context.Context, limit int) ([]CrashReport, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+crashColumns+` FROM crash_reports ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list crash reports: %w", err)
	}
	defer rows.Close()
	out := make([]CrashReport, 0, limit)
	for rows.Next() {
		r, err := scanCrash(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetCrashReport 按 ID 读取崩溃上报。
func (s *Store) GetCrashReport(ctx context.Context, id int64) (*CrashReport, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+crashColumns+` FROM crash_reports WHERE id = $1`, id)
	return scanCrash(row)
}

// UpdateCrashIssue 回填 Issue 信息与状态。
func (s *Store) UpdateCrashIssue(ctx context.Context, id int64, issueNumber int, issueURL, status, rejectReason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE crash_reports
 SET issue_number = $2, issue_url = $3, status = $4, reject_reason = $5, updated_at = now() WHERE id = $1`,
		id, issueNumber, issueURL, status, rejectReason)
	if err != nil {
		return fmt.Errorf("store: update crash report: %w", err)
	}
	return nil
}
