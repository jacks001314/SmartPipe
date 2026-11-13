package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"smartpipe/backend/internal/testlab"
	"time"
)

func init() {
	migrations = append(migrations, migration{version: 45, name: "desktop_test_machines", sql: `
CREATE TABLE desktop_test_machines (
 id TEXT PRIMARY KEY,
 platform TEXT NOT NULL,
 token_sha256 TEXT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 generation BIGINT NOT NULL DEFAULT 0,
 run_id TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,
 last_seen TIMESTAMPTZ
);
ALTER TABLE desktop_test_runs ADD COLUMN machine_id TEXT NOT NULL DEFAULT '';
ALTER TABLE desktop_test_runs ADD COLUMN lease_generation BIGINT NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX desktop_test_machine_run_unique ON desktop_test_machines(run_id) WHERE run_id <> '';
`})
}
func (s *Store) RegisterDesktopMachine(ctx context.Context, id, platform, digest string) error {
	if id == "" || platform == "" || len(digest) != 64 {
		return testlab.ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO desktop_test_machines(id,platform,token_sha256) VALUES($1,$2,$3)`, id, platform, digest)
	return err
}
func (s *Store) AuthenticateDesktopMachine(ctx context.Context, id, token string) error {
	if len(token) < 32 {
		return testlab.ErrInvalid
	}
	var digest string
	if err := s.db.QueryRowContext(ctx, `SELECT token_sha256 FROM desktop_test_machines WHERE id=$1 AND enabled=TRUE`, id).Scan(&digest); err != nil {
		return testlab.ErrInvalid
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(testlab.TokenDigest(token))) != 1 {
		return testlab.ErrInvalid
	}
	return nil
}
func (s *Store) ListDesktopMachines(ctx context.Context) ([]testlab.Machine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,platform,enabled,generation,run_id,lease_until,last_seen FROM desktop_test_machines ORDER BY id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []testlab.Machine{}
	for rows.Next() {
		var m testlab.Machine
		if err = rows.Scan(&m.ID, &m.Platform, &m.Enabled, &m.Generation, &m.RunID, &m.LeaseUntil, &m.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Claim serializes on the machine before selecting a run. Expired busy machines
// are NOT automatically reused: an old desktop process may still be performing input.
func (s *Store) ClaimDesktopRun(ctx context.Context, machineID string, scenarios ...string) (*testlab.Lease, error) {
	if len(scenarios) == 0 {
		scenarios = []string{"desktop-observe@1"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var platform, busy string
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT platform,run_id,generation FROM desktop_test_machines WHERE id=$1 AND enabled=TRUE FOR UPDATE`, machineID).Scan(&platform, &busy, &generation)
	if err != nil {
		return nil, err
	}
	if busy != "" {
		return nil, testlab.ErrState
	}
	var run testlab.Run
	var spec []byte
	err = tx.QueryRowContext(ctx, `SELECT id,spec,created_at FROM desktop_test_runs WHERE status='QUEUED' AND spec->>'platform'=$1 AND (spec->>'scenario')||'@'||(spec->>'scenario_version')=ANY($2) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, platform, scenarios).Scan(&run.ID, &spec, &run.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(spec, &run.Spec); err != nil {
		return nil, err
	}
	generation++
	var until time.Time
	err = tx.QueryRowContext(ctx, `UPDATE desktop_test_machines SET run_id=$2,generation=$3,lease_until=now()+interval '60 seconds',last_seen=now() WHERE id=$1 RETURNING lease_until`, machineID, run.ID, generation).Scan(&until)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE desktop_test_runs SET status='RUNNING',machine_id=$2,lease_generation=$3 WHERE id=$1`, run.ID, machineID, generation)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	run.Status = "RUNNING"
	return &testlab.Lease{MachineID: machineID, Generation: generation, ExpiresAt: until, Run: &run}, nil
}
func (s *Store) HeartbeatDesktopRun(ctx context.Context, machineID, runID string, generation int64) (time.Time, error) {
	var until time.Time
	err := s.db.QueryRowContext(ctx, `UPDATE desktop_test_machines m SET last_seen=now(),lease_until=now()+interval '60 seconds'
 WHERE m.id=$1 AND m.run_id=$2 AND m.generation=$3 AND m.enabled=TRUE AND m.lease_until>now()
 AND EXISTS(SELECT 1 FROM desktop_test_runs r WHERE r.id=$2 AND r.status='RUNNING' AND r.machine_id=$1 AND r.lease_generation=$3)
 RETURNING lease_until`, machineID, runID, generation).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return until, testlab.ErrState
	}
	return until, err
}

// Finish verifies and locks the lease, then CASes the run. A cancelled or expired
// assignment cannot submit success. Release follows separately after local cleanup.
func (s *Store) FinishDesktopRun(ctx context.Context, machineID, runID string, generation int64, report testlab.Report) (*testlab.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM desktop_test_machines WHERE id=$1 AND run_id=$2 AND generation=$3 AND enabled=TRUE AND lease_until>now() FOR UPDATE`, machineID, runID, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, testlab.ErrState
	}
	if err != nil {
		return nil, err
	}
	var specJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT spec FROM desktop_test_runs WHERE id=$1 AND machine_id=$2 AND lease_generation=$3 AND status='RUNNING' FOR UPDATE`, runID, machineID, generation).Scan(&specJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, testlab.ErrState
	}
	if err != nil {
		return nil, err
	}
	var spec testlab.Spec
	if err = json.Unmarshal(specJSON, &spec); err != nil {
		return nil, err
	}
	status, err := testlab.Evaluate(spec, report)
	if err != nil {
		return nil, err
	}
	if err = validateReportEvidence(ctx, tx, runID, report); err != nil {
		return nil, err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE desktop_test_runs SET status=$2,report=$3,finished_at=now() WHERE id=$1`, runID, status, data)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDesktopTestRun(ctx, runID)
}

// Release is an acknowledgement that all local work stopped. It can release an
// expired lease, but cannot release a newer generation or a RUNNING task.
func (s *Store) ReleaseDesktopMachine(ctx context.Context, machineID, runID string, generation int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_test_machines m SET run_id='',lease_until=NULL,last_seen=now()
 WHERE id=$1 AND run_id=$2 AND generation=$3
 AND EXISTS(SELECT 1 FROM desktop_test_runs WHERE id=$2 AND status IN ('PASS','FAIL','BLOCKED','CANCELLED'))`, machineID, runID, generation)
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
