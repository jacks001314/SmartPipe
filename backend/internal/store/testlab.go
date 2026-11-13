package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"smartpipe/backend/internal/testlab"
	"time"
)

func init() {
	migrations = append(migrations, migration{version: 48, name: "desktop_test_runs", sql: `
CREATE TABLE IF NOT EXISTS desktop_test_runs (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 spec JSONB NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('QUEUED','RUNNING','PASS','FAIL','BLOCKED','CANCELLED')),
 report JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS desktop_test_runs_project_idx ON desktop_test_runs(project_id,created_at DESC);
`})
}

func (s *Store) CreateDesktopTestRun(ctx context.Context, id string, spec testlab.Spec) (*testlab.Run, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, testlab.ErrInvalid
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO desktop_test_runs(id,project_id,spec,status) VALUES($1,$2,$3,'QUEUED')`, id, spec.ProjectID, data)
	if err != nil {
		return nil, fmt.Errorf("create desktop test run: %w", err)
	}
	return s.GetDesktopTestRun(ctx, id)
}
func (s *Store) GetDesktopTestRun(ctx context.Context, id string) (*testlab.Run, error) {
	var r testlab.Run
	var spec, report []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,spec,status,report,created_at,finished_at FROM desktop_test_runs WHERE id=$1`, id).Scan(&r.ID, &spec, &r.Status, &report, &r.CreatedAt, &r.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(spec, &r.Spec); err != nil {
		return nil, err
	}
	if len(report) > 0 {
		if err = json.Unmarshal(report, &r.Report); err != nil {
			return nil, err
		}
	}
	return &r, nil
}
func (s *Store) StartDesktopTestRun(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_test_runs SET status='RUNNING' WHERE id=$1 AND status='QUEUED'`, id)
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
func (s *Store) CancelDesktopTestRun(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_test_runs SET status='CANCELLED',finished_at=now() WHERE id=$1 AND status IN ('QUEUED','RUNNING')`, id)
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

// Finalize uses a compare-and-set, so cancellation wins against late reports
// and terminal reports cannot be overwritten. The evaluator computes status.
func (s *Store) CompleteDesktopTestRun(ctx context.Context, id string, report testlab.Report) (*testlab.Run, error) {
	run, err := s.GetDesktopTestRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.Status != "RUNNING" {
		return nil, testlab.ErrState
	}
	status, err := testlab.Evaluate(run.Spec, report)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_test_runs SET status=$2,report=$3,finished_at=$4 WHERE id=$1 AND status='RUNNING' AND machine_id=''`, id, status, data, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, testlab.ErrState
	}
	return s.GetDesktopTestRun(ctx, id)
}
