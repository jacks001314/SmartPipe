package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Release 是一次发版构建的归档记录。
type Release struct {
	ID             int64           `json:"id"`
	ProjectID      *string         `json:"project_id"`
	Version        string          `json:"version"`
	CommitSHA      string          `json:"commit_sha"`
	OK             bool            `json:"ok"`
	Status         string          `json:"status"`
	Manifest       json.RawMessage `json:"manifest"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	Signature      string          `json:"signature"`
	Artifacts      json.RawMessage `json:"artifacts"`
	OutputDir      string          `json:"output_dir"`
	Error          string          `json:"error"`
	DurationMS     int             `json:"duration_ms"`
	CreatedAt      time.Time       `json:"created_at"`
}

// CreateRelease 写入发版记录。
func (s *Store) CreateRelease(ctx context.Context, r Release) (*Release, error) {
	if len(r.Manifest) == 0 {
		r.Manifest = json.RawMessage("{}")
	}
	if len(r.Artifacts) == 0 {
		r.Artifacts = json.RawMessage("[]")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO releases (project_id, version, commit_sha, ok, status, manifest, manifest_sha256, signature, artifacts, output_dir, error, duration_ms)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
RETURNING id, created_at`,
		r.ProjectID, r.Version, r.CommitSHA, r.OK, r.Status, r.Manifest, r.ManifestSHA256, r.Signature,
		r.Artifacts, r.OutputDir, r.Error, r.DurationMS)
	if err := row.Scan(&r.ID, &r.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: create release: %w", err)
	}
	return &r, nil
}

// ListReleases 返回最近发版记录。
func (s *Store) ListReleases(ctx context.Context, limit int) ([]Release, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, version, commit_sha, ok, status, manifest, manifest_sha256, signature, artifacts, output_dir, error, duration_ms, created_at
  FROM releases ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list releases: %w", err)
	}
	defer rows.Close()
	out := make([]Release, 0, limit)
	for rows.Next() {
		var (
			r       Release
			project sql.NullString
		)
		if err := rows.Scan(&r.ID, &project, &r.Version, &r.CommitSHA, &r.OK, &r.Status, &r.Manifest,
			&r.ManifestSHA256, &r.Signature, &r.Artifacts, &r.OutputDir, &r.Error, &r.DurationMS, &r.CreatedAt); err != nil {
			return nil, err
		}
		if project.Valid {
			v := project.String
			r.ProjectID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRelease 按 ID 读取发版记录。
func (s *Store) GetRelease(ctx context.Context, id int64) (*Release, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, version, commit_sha, ok, status, manifest, manifest_sha256, signature, artifacts, output_dir, error, duration_ms, created_at
  FROM releases WHERE id = $1`, id)
	var (
		r       Release
		project sql.NullString
	)
	err := row.Scan(&r.ID, &project, &r.Version, &r.CommitSHA, &r.OK, &r.Status, &r.Manifest,
		&r.ManifestSHA256, &r.Signature, &r.Artifacts, &r.OutputDir, &r.Error, &r.DurationMS, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get release: %w", err)
	}
	if project.Valid {
		v := project.String
		r.ProjectID = &v
	}
	return &r, nil
}
