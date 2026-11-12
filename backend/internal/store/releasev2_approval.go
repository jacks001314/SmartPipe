package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"smartpipe/backend/internal/releasev2"
)

func (s *Store) CreateApproval(ctx context.Context, a releasev2.Approval) (*releasev2.Approval, error) {
	if a.ID == "" {
		a.ID = releasev2.NewID("rappr")
	}
	if a.Kind == "" {
		a.Kind = "publish"
	}
	if a.State == "" {
		a.State = "approved"
	}
	var expires sql.NullTime
	if a.ExpiresAt != nil {
		expires = sql.NullTime{Time: *a.ExpiresAt, Valid: true}
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_approvals (id, run_id, kind, actor, reason, content_sha256, state, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING created_at`, a.ID, a.RunID, a.Kind, a.Actor, a.Reason, a.ContentSHA256, a.State, expires)
	if err := row.Scan(&a.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: create release approval: %w", err)
	}
	return &a, nil
}

func (s *Store) ListApprovals(ctx context.Context, runID string, limit int) ([]releasev2.Approval, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, kind, actor, reason, content_sha256, state, expires_at, created_at
  FROM release_v2_approvals WHERE run_id=$1 ORDER BY created_at DESC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list release approvals: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Approval{}
	for rows.Next() {
		var a releasev2.Approval
		var expires sql.NullTime
		if err := rows.Scan(&a.ID, &a.RunID, &a.Kind, &a.Actor, &a.Reason, &a.ContentSHA256, &a.State, &expires, &a.CreatedAt); err != nil {
			return nil, err
		}
		if expires.Valid {
			t := expires.Time
			a.ExpiresAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LatestValidApproval returns the newest approval for the run that is still
// approved, unexpired and bound to the same content hash.
func (s *Store) LatestValidApproval(ctx context.Context, runID, kind, contentSHA string) (*releasev2.Approval, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, run_id, kind, actor, reason, content_sha256, state, expires_at, created_at
  FROM release_v2_approvals
 WHERE run_id=$1 AND kind=$2 AND content_sha256=$3 AND state='approved'
   AND (expires_at IS NULL OR expires_at > now())
 ORDER BY created_at DESC LIMIT 1`, runID, kind, contentSHA)
	var a releasev2.Approval
	var expires sql.NullTime
	if err := row.Scan(&a.ID, &a.RunID, &a.Kind, &a.Actor, &a.Reason, &a.ContentSHA256, &a.State, &expires, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get release approval: %w", err)
	}
	if expires.Valid {
		t := expires.Time
		a.ExpiresAt = &t
	}
	return &a, nil
}
