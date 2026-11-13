package store

import (
	"context"
	"database/sql"
	"smartpipe/backend/internal/testlab"
)

// Disabling fences API access and cancels the assigned run atomically. It does
// not free the desktop: local processes must be stopped before recovery.
func (s *Store) SetDesktopMachineEnabled(ctx context.Context, id string, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID string
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM desktop_test_machines WHERE id=$1 FOR UPDATE`, id).Scan(&runID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if enabled && runID != "" {
		return testlab.ErrState
	}
	if !enabled && runID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE desktop_test_runs SET status='CANCELLED',finished_at=now() WHERE id=$1 AND status IN ('QUEUED','RUNNING')`, runID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE desktop_test_machines SET enabled=$2 WHERE id=$1`, id, enabled); err != nil {
		return err
	}
	return tx.Commit()
}

// Recovery requires an explicit admin acknowledgement and exact generation.
// This cannot prove OS cleanup; the caller must independently inspect the host.
func (s *Store) RecoverDesktopMachine(ctx context.Context, id string, generation int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_test_machines m SET run_id='',lease_until=NULL,generation=generation+1
 WHERE id=$1 AND generation=$2 AND enabled=FALSE
 AND (run_id='' OR EXISTS(SELECT 1 FROM desktop_test_runs r WHERE r.id=m.run_id AND r.status IN ('PASS','FAIL','BLOCKED','CANCELLED')))`, id, generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return testlab.ErrState
	}
	return nil
}
