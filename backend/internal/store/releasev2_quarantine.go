package store

import (
	"context"
	"encoding/json"
	"fmt"

	"smartpipe/backend/internal/releasev2"
)

// AddQuarantine records a failed artifact or channel advance that must not be
// served to clients until it is retried or rolled back.
func (s *Store) AddQuarantine(ctx context.Context, q releasev2.Quarantine) (*releasev2.Quarantine, error) {
	if q.RunID == "" {
		return nil, fmt.Errorf("store: quarantine needs a run id")
	}
	state := q.State
	if state == "" {
		state = "quarantined"
	}
	detail := q.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("store: encode quarantine detail: %w", err)
	}
	var artifactID any
	if q.ArtifactID > 0 {
		artifactID = q.ArtifactID
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_quarantines (run_id, artifact_id, service, channel, external_id, reason, detail, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at, resolved_at`,
		q.RunID, artifactID, q.Service, q.Channel, q.ExternalID, q.Reason, raw, state)
	q.State = state
	q.Detail = detail
	if err := row.Scan(&q.ID, &q.CreatedAt, &q.ResolvedAt); err != nil {
		return nil, fmt.Errorf("store: add release quarantine: %w", err)
	}
	return &q, nil
}

// ListQuarantines returns the newest quarantines first.
func (s *Store) ListQuarantines(ctx context.Context, runID string) ([]releasev2.Quarantine, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, COALESCE(artifact_id, 0), service, channel, external_id, reason,
       COALESCE(detail, '{}'::jsonb), state, created_at, resolved_at
  FROM release_v2_quarantines WHERE run_id=$1 ORDER BY created_at DESC, id DESC`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list release quarantines: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Quarantine{}
	for rows.Next() {
		var q releasev2.Quarantine
		var raw []byte
		if err := rows.Scan(&q.ID, &q.RunID, &q.ArtifactID, &q.Service, &q.Channel,
			&q.ExternalID, &q.Reason, &raw, &q.State, &q.CreatedAt, &q.ResolvedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &q.Detail)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
