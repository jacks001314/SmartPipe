package store

import (
	"context"
	"database/sql"
	"errors"

	"smartpipe/backend/internal/testlab"
)

// The console shows the engine timeline next to the checks it produced, so the
// runner pushes coarse-grained events (claim, story, hotswap, check, report)
// instead of the whole agent transcript.
func init() {
	migrations = append(migrations, migration{version: 60, name: "test_session_events", sql: `
CREATE TABLE IF NOT EXISTS test_session_events (
 session_id TEXT NOT NULL REFERENCES test_sessions(id) ON DELETE CASCADE,
 seq INT NOT NULL,
 kind TEXT NOT NULL,
 message TEXT NOT NULL,
 at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id, seq)
);
CREATE INDEX IF NOT EXISTS test_session_events_session_idx ON test_session_events(session_id, seq);
`})
}

// AppendTestSessionEvents validates the machine lease once and upserts the
// batch, so a retried sync stays idempotent.
func (s *Store) AppendTestSessionEvents(ctx context.Context, machineID, sessionID string, generation int64,
	events []testlab.SessionEvent) error {
	if len(events) == 0 {
		return nil
	}
	if len(events) > testlab.MaxSessionEvents {
		return testlab.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM desktop_test_machines
 WHERE id=$1 AND session_id=$2 AND session_generation=$3 AND enabled=TRUE AND lease_until>now() FOR UPDATE`,
		machineID, sessionID, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return testlab.ErrState
	}
	if err != nil {
		return err
	}
	for _, event := range events {
		if err = event.Validate(); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO test_session_events(session_id,seq,kind,message,at)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(session_id,seq) DO NOTHING`,
			sessionID, event.Seq, event.Kind, event.Message, event.At); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTestSessionEvents returns the timeline ordered by seq starting after
// afterSeq, which lets the console poll incrementally.
func (s *Store) ListTestSessionEvents(ctx context.Context, sessionID string, afterSeq, limit int) ([]testlab.SessionEvent, error) {
	if limit <= 0 || limit > testlab.MaxSessionEvents {
		limit = testlab.MaxSessionEvents
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,kind,message,at FROM test_session_events
 WHERE session_id=$1 AND seq>$2 ORDER BY seq LIMIT $3`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []testlab.SessionEvent{}
	for rows.Next() {
		var event testlab.SessionEvent
		if err = rows.Scan(&event.Seq, &event.Kind, &event.Message, &event.At); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
