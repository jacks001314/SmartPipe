package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"smartpipe/backend/internal/releasev2"
)

// UpdateRunNotes stores the release notes for a run while it is still open.
func (s *Store) UpdateRunNotes(ctx context.Context, runID, notes, notesSHA string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET notes_md=$2, notes_sha256=$3, updated_at=now()
WHERE id=$1 AND status NOT IN ('published','built','failed','cancelled')`, runID, notes, notesSHA)
	if err != nil {
		return false, fmt.Errorf("store: update release v2 notes: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AppendLogChunk persists one chunk of build output as an append-only event.
func (s *Store) AppendLogChunk(ctx context.Context, runID, targetID, chunk string, seq int, actor string) error {
	return s.AppendEvent(ctx, releasev2.Event{
		RunID: runID, Actor: firstStoreNonEmpty(actor, "executor"), Kind: "target.log", TargetID: targetID,
		Summary: "build log", Detail: map[string]any{"seq": seq, "chunk": chunk},
	})
}

// ListLogChunks returns build log events after the given event id cursor.
func (s *Store) ListLogChunks(ctx context.Context, runID, targetID string, after int64, limit int) ([]releasev2.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `SELECT id, run_id, actor, kind, target_id, summary, detail, created_at
  FROM release_v2_events WHERE run_id=$1 AND kind='target.log' AND id>$2`
	args := []any{runID, after}
	if strings.TrimSpace(targetID) != "" {
		query += ` AND target_id=$3`
		args = append(args, strings.TrimSpace(targetID))
	}
	query += fmt.Sprintf(` ORDER BY id LIMIT %d`, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list release v2 log chunks: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Event{}
	for rows.Next() {
		var e releasev2.Event
		var detail []byte
		if err := rows.Scan(&e.ID, &e.RunID, &e.Actor, &e.Kind, &e.TargetID, &e.Summary, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkTargetStatus moves a target between non-leased bookkeeping states.
func (s *Store) MarkTargetStatus(ctx context.Context, runID, targetID string, from, to releasev2.TargetStatus) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET status=$4, updated_at=now()
WHERE run_id=$1 AND target_id=$2 AND status=$3`, runID, targetID, from, to)
	if err != nil {
		return false, fmt.Errorf("store: mark release v2 target status: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RequeueExpiredLeases returns targets whose lease expired without a finish
// call to the retry queue so another executor attempt can pick them up.
func (s *Store) RequeueExpiredLeases(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET
  status='retry_queued', attempt=attempt+1, lease_owner='', lease_expires_at=NULL,
  error_code='lease_expired', error_message='executor lease expired', started_at=NULL, finished_at=NULL,
  updated_at=now()
WHERE status IN ('leased','building','verifying') AND lease_expires_at IS NOT NULL AND lease_expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("store: requeue expired release v2 leases: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetTarget returns a single target row by run and stable target id.
func (s *Store) GetTarget(ctx context.Context, runID, targetID string) (*releasev2.Target, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, run_id, target_id, os, arch, executor_id, required, status, attempt, lease_owner,
       lease_expires_at, build_command, sign_command, verify_command, artifact_rules, error_code, error_message,
       started_at, finished_at, created_at, updated_at
  FROM release_v2_targets WHERE run_id=$1 AND target_id=$2`, runID, targetID)
	var t releasev2.Target
	var lease, started, finished sql.NullTime
	var rules []byte
	if err := row.Scan(&t.ID, &t.RunID, &t.TargetID, &t.OS, &t.Arch, &t.ExecutorID, &t.Required,
		&t.Status, &t.Attempt, &t.LeaseOwner, &lease, &t.BuildCommand, &t.SignCommand, &t.VerifyCommand, &rules,
		&t.ErrorCode, &t.ErrorMessage, &started, &finished, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get release v2 target: %w", err)
	}
	_ = json.Unmarshal(rules, &t.Artifacts)
	t.LeaseExpiresAt = nullTimePtr(lease)
	t.StartedAt = nullTimePtr(started)
	t.FinishedAt = nullTimePtr(finished)
	return &t, nil
}

// GetArtifact returns one artifact by run, target, role and name.
func (s *Store) GetArtifact(ctx context.Context, runID, targetID, role, name string) (*releasev2.Artifact, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, run_id, target_id, attempt, role, name, content_type, size, sha256, storage_key,
       platform_signed, manifest_signed, created_at
  FROM release_v2_artifacts WHERE run_id=$1 AND target_id=$2 AND role=$3 AND name=$4`,
		runID, targetID, role, name)
	var a releasev2.Artifact
	if err := row.Scan(&a.ID, &a.RunID, &a.TargetID, &a.Attempt, &a.Role, &a.Name, &a.ContentType,
		&a.Size, &a.SHA256, &a.StorageKey, &a.PlatformSigned, &a.ManifestSigned, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get release v2 artifact: %w", err)
	}
	return &a, nil
}
