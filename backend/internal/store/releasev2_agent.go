package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"smartpipe/backend/internal/releasev2"
)

const agentSessionColumns = `id, run_id, project_id, status, actor, model, thread_id, turn_id,
       summary, last_error, token_id, started_at, finished_at, created_at, updated_at`

func scanAgentSession(row interface{ Scan(...any) error }) (*releasev2.AgentSession, error) {
	var s releasev2.AgentSession
	var finished sql.NullTime
	if err := row.Scan(&s.ID, &s.RunID, &s.ProjectID, &s.Status, &s.Actor, &s.Model, &s.ThreadID, &s.TurnID,
		&s.Summary, &s.LastError, &s.TokenID, &s.StartedAt, &finished, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if finished.Valid {
		t := finished.Time
		s.FinishedAt = &t
	}
	return &s, nil
}

// CreateAgentSession registers a release agent session before the Codex
// process starts so the console can show it even if the process never runs.
func (s *Store) CreateAgentSession(ctx context.Context, in *releasev2.AgentSession) error {
	if in.Status == "" {
		in.Status = releasev2.AgentSessionStarting
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_agent_sessions (id, run_id, project_id, status, actor, model, thread_id, turn_id, summary, last_error, token_id, started_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
RETURNING started_at, created_at, updated_at`,
		in.ID, in.RunID, in.ProjectID, in.Status, in.Actor, in.Model, in.ThreadID, in.TurnID, in.Summary, in.LastError, in.TokenID)
	if err := row.Scan(&in.StartedAt, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return fmt.Errorf("store: create release agent session: %w", err)
	}
	return nil
}

// UpdateAgentSession writes the mutable fields of a session.
func (s *Store) UpdateAgentSession(ctx context.Context, in *releasev2.AgentSession) error {
	var finished any
	if in.FinishedAt != nil {
		finished = *in.FinishedAt
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE release_v2_agent_sessions SET
  status=$2, model=$3, thread_id=$4, turn_id=$5, summary=$6, last_error=$7, token_id=$8,
  finished_at=$9, updated_at=now()
WHERE id=$1`,
		in.ID, in.Status, in.Model, in.ThreadID, in.TurnID, in.Summary, in.LastError, in.TokenID, finished)
	if err != nil {
		return fmt.Errorf("store: update release agent session: %w", err)
	}
	return nil
}

func (s *Store) GetAgentSession(ctx context.Context, id string) (*releasev2.AgentSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentSessionColumns+` FROM release_v2_agent_sessions WHERE id=$1`, id)
	out, err := scanAgentSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get release agent session: %w", err)
	}
	return out, nil
}

// LatestAgentSession returns the newest session for a run, if any.
func (s *Store) LatestAgentSession(ctx context.Context, runID string) (*releasev2.AgentSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentSessionColumns+`
  FROM release_v2_agent_sessions WHERE run_id=$1 ORDER BY created_at DESC LIMIT 1`, runID)
	out, err := scanAgentSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: latest release agent session: %w", err)
	}
	return out, nil
}

// ListAgentSessions returns recent sessions of a run, newest first.
func (s *Store) ListAgentSessions(ctx context.Context, runID string, limit int) ([]releasev2.AgentSession, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentSessionColumns+`
  FROM release_v2_agent_sessions WHERE run_id=$1 ORDER BY created_at DESC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list release agent sessions: %w", err)
	}
	defer rows.Close()
	out := []releasev2.AgentSession{}
	for rows.Next() {
		item, err := scanAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// CloseStaleAgentSessions marks sessions left behind by a restart as failed so
// the console does not show a phantom running agent forever.
func (s *Store) CloseStaleAgentSessions(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_agent_sessions SET status='failed', last_error=COALESCE(NULLIF(last_error,''),'agent process lost'),
  finished_at=now(), updated_at=now()
WHERE status IN ('starting','running')`)
	if err != nil {
		return 0, fmt.Errorf("store: close stale release agent sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
