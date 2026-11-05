package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ReleaseRun 是一次完整发版运行（候选集 + 构建 + 发布 + 通知）。
type ReleaseRun struct {
	ID             int64           `json:"id"`
	ProjectID      *string         `json:"project_id"`
	Version        string          `json:"version"`
	CommitSHA      string          `json:"commit_sha"`
	Branch         string          `json:"branch"`
	Status         string          `json:"status"`
	Trigger        string          `json:"trigger"`
	Candidates     json.RawMessage `json:"candidates"`
	Notes          string          `json:"notes"`
	ReleaseURL     string          `json:"release_url"`
	Tag            string          `json:"tag"`
	ReleaseID      int64           `json:"release_id"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	OutputDir      string          `json:"output_dir"`
	Artifacts      json.RawMessage `json:"artifacts"`
	BuildOutput    string          `json:"build_output"`
	Error          string          `json:"error"`
	NotifyStatus   string          `json:"notify_status"`
	NotifyError    string          `json:"notify_error"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
	DurationMS     int             `json:"duration_ms"`
}

const releaseRunColumns = `id, project_id, version, commit_sha, branch, status, trigger, candidates, notes,
release_url, tag, release_id, manifest_sha256, output_dir, artifacts, build_output, error,
notify_status, notify_error, started_at, finished_at, duration_ms`

// CreateReleaseRun 写入一次发版运行记录。
func (s *Store) CreateReleaseRun(ctx context.Context, r ReleaseRun) (*ReleaseRun, error) {
	if len(r.Candidates) == 0 {
		r.Candidates = json.RawMessage("[]")
	}
	if len(r.Artifacts) == 0 {
		r.Artifacts = json.RawMessage("[]")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_runs (project_id, version, commit_sha, branch, status, trigger, candidates, notes,
    release_url, tag, release_id, manifest_sha256, output_dir, artifacts, build_output, error,
    notify_status, notify_error, started_at, finished_at, duration_ms)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
RETURNING id`,
		r.ProjectID, r.Version, r.CommitSHA, r.Branch, r.Status, r.Trigger, r.Candidates, r.Notes,
		r.ReleaseURL, r.Tag, r.ReleaseID, r.ManifestSHA256, r.OutputDir, r.Artifacts, r.BuildOutput, r.Error,
		r.NotifyStatus, r.NotifyError, r.StartedAt, r.FinishedAt, r.DurationMS)
	if err := row.Scan(&r.ID); err != nil {
		return nil, fmt.Errorf("store: create release run: %w", err)
	}
	return &r, nil
}

// UpdateReleaseRun 更新一次发版运行的状态与结果。
func (s *Store) UpdateReleaseRun(ctx context.Context, r ReleaseRun) error {
	if len(r.Candidates) == 0 {
		r.Candidates = json.RawMessage("[]")
	}
	if len(r.Artifacts) == 0 {
		r.Artifacts = json.RawMessage("[]")
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE release_runs SET status=$2, candidates=$3, notes=$4, release_url=$5, tag=$6, release_id=$7,
    manifest_sha256=$8, output_dir=$9, artifacts=$10, build_output=$11, error=$12,
    notify_status=$13, notify_error=$14, finished_at=$15, duration_ms=$16
 WHERE id=$1`,
		r.ID, r.Status, r.Candidates, r.Notes, r.ReleaseURL, r.Tag, r.ReleaseID,
		r.ManifestSHA256, r.OutputDir, r.Artifacts, r.BuildOutput, r.Error,
		r.NotifyStatus, r.NotifyError, r.FinishedAt, r.DurationMS)
	if err != nil {
		return fmt.Errorf("store: update release run: %w", err)
	}
	return nil
}

// ListReleaseRuns 返回最近发版运行。
func (s *Store) ListReleaseRuns(ctx context.Context, limit int) ([]ReleaseRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseRunColumns+` FROM release_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list release runs: %w", err)
	}
	defer rows.Close()
	out := make([]ReleaseRun, 0, limit)
	for rows.Next() {
		r, err := scanReleaseRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetReleaseRun 按 ID 读取发版运行。
func (s *Store) GetReleaseRun(ctx context.Context, id int64) (*ReleaseRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseRunColumns+` FROM release_runs WHERE id = $1`, id)
	r, err := scanReleaseRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanReleaseRun(row rowScanner) (*ReleaseRun, error) {
	var (
		r        ReleaseRun
		project  sql.NullString
		finished sql.NullTime
	)
	err := row.Scan(&r.ID, &project, &r.Version, &r.CommitSHA, &r.Branch, &r.Status, &r.Trigger,
		&r.Candidates, &r.Notes, &r.ReleaseURL, &r.Tag, &r.ReleaseID, &r.ManifestSHA256, &r.OutputDir,
		&r.Artifacts, &r.BuildOutput, &r.Error, &r.NotifyStatus, &r.NotifyError, &r.StartedAt, &finished, &r.DurationMS)
	if err != nil {
		return nil, err
	}
	if project.Valid {
		v := project.String
		r.ProjectID = &v
	}
	if finished.Valid {
		t := finished.Time
		r.FinishedAt = &t
	}
	return &r, nil
}

// HasReleaseRun 判断某个项目是否已经有过指定版本的发版运行。
func (s *Store) HasReleaseRun(ctx context.Context, projectID, version string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM release_runs WHERE project_id = $1 AND version = $2)`,
		projectID, version).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: check release run: %w", err)
	}
	return exists, nil
}

// LatestReleasedVersion 返回项目最近一次成功发布的版本号（没有则空串）。
func (s *Store) LatestReleasedVersion(ctx context.Context, projectID string) (string, error) {
	var version string
	err := s.db.QueryRowContext(ctx, `
SELECT version FROM release_runs
WHERE project_id = $1 AND status IN ('released','released_with_warnings')
ORDER BY id DESC LIMIT 1`, projectID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: latest released version: %w", err)
	}
	return version, nil
}
