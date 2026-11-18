package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"smartpipe/backend/internal/testlab"
	"strings"
	"time"
)

// ErrSessionTerminal reports that the leased session is no longer RUNNING. The
// runner must stop issuing commands and report or release.
var ErrSessionTerminal = errors.New("store: test session terminal")

func init() {
	migrations = append(migrations, migration{version: 57, name: "agent_test_sessions", sql: `
CREATE TABLE IF NOT EXISTS test_sessions (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 spec JSONB NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('QUEUED','RUNNING','PASS','FAIL','BLOCKED','CANCELLED')),
 agent_token_sha256 TEXT NOT NULL,
 machine_id TEXT NOT NULL DEFAULT '',
 lease_generation BIGINT NOT NULL DEFAULT 0,
 lease_until TIMESTAMPTZ,
 deadline TIMESTAMPTZ,
 finish_requested BOOLEAN NOT NULL DEFAULT FALSE,
 agent_verdict TEXT NOT NULL DEFAULT '',
 verdict TEXT NOT NULL DEFAULT '',
 summary TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 started_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS test_sessions_project_idx ON test_sessions(project_id,created_at DESC);
CREATE INDEX IF NOT EXISTS test_sessions_queue_idx ON test_sessions(status,created_at);
CREATE TABLE IF NOT EXISTS test_session_commands (
 id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL REFERENCES test_sessions(id) ON DELETE CASCADE,
 seq BIGSERIAL NOT NULL,
 kind TEXT NOT NULL,
 args JSONB NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('PENDING','RUNNING','DONE','FAILED','CANCELLED')),
 result JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 started_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS test_session_commands_pending_idx ON test_session_commands(session_id,status,seq);
CREATE TABLE IF NOT EXISTS test_session_checks (
 session_id TEXT NOT NULL REFERENCES test_sessions(id) ON DELETE CASCADE,
 check_id TEXT NOT NULL,
 status TEXT NOT NULL,
 evidence TEXT NOT NULL DEFAULT '',
 note TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id,check_id)
);
CREATE TABLE IF NOT EXISTS test_session_evidence (
 session_id TEXT NOT NULL REFERENCES test_sessions(id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL,
 mime_type TEXT NOT NULL,
 payload BYTEA NOT NULL CHECK(octet_length(payload) <= 8388608),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id,sha256)
);
ALTER TABLE desktop_test_machines ADD COLUMN IF NOT EXISTS session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE desktop_test_machines ADD COLUMN IF NOT EXISTS session_generation BIGINT NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS desktop_test_machine_session_unique ON desktop_test_machines(session_id) WHERE session_id <> '';
`})
}

func (s *Store) CreateTestSession(ctx context.Context, id string, spec testlab.SessionSpec, tokenDigest string) (*testlab.Session, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.TimeoutSeconds == 0 {
		spec.TimeoutSeconds = 30 * 60
	}
	if spec.Kind == "" {
		spec.Kind = "story"
	}
	if id == "" || len(tokenDigest) != 64 {
		return nil, testlab.ErrInvalid
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO test_sessions(id,project_id,spec,status,agent_token_sha256) VALUES($1,$2,$3,'QUEUED',$4)`,
		id, spec.ProjectID, data, tokenDigest)
	if err != nil {
		return nil, fmt.Errorf("create test session: %w", err)
	}
	return s.GetTestSession(ctx, id)
}

func (s *Store) GetTestSession(ctx context.Context, id string) (*testlab.Session, error) {
	var out testlab.Session
	var spec []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,spec,status,machine_id,verdict,summary,created_at,started_at,finished_at,deadline
 FROM test_sessions WHERE id=$1`, id).Scan(&out.ID, &spec, &out.Status, &out.MachineID, &out.Verdict, &out.Summary,
		&out.CreatedAt, &out.StartedAt, &out.FinishedAt, &out.Deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(spec, &out.Spec); err != nil {
		return nil, err
	}
	checks, err := s.ListTestSessionChecks(ctx, id)
	if err != nil {
		return nil, err
	}
	out.Checks = checks
	return &out, nil
}

func (s *Store) ListTestSessions(ctx context.Context, projectID string) ([]testlab.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,spec,status,machine_id,verdict,summary,created_at,started_at,finished_at,deadline
 FROM test_sessions WHERE ($1='' OR project_id=$1) ORDER BY created_at DESC LIMIT 100`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []testlab.Session{}
	for rows.Next() {
		var item testlab.Session
		var spec []byte
		if err = rows.Scan(&item.ID, &spec, &item.Status, &item.MachineID, &item.Verdict, &item.Summary,
			&item.CreatedAt, &item.StartedAt, &item.FinishedAt, &item.Deadline); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(spec, &item.Spec); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListTestSessionChecks(ctx context.Context, id string) ([]testlab.SessionCheck, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT check_id,status,evidence,note,updated_at FROM test_session_checks WHERE session_id=$1 ORDER BY check_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []testlab.SessionCheck{}
	for rows.Next() {
		var c testlab.SessionCheck
		if err = rows.Scan(&c.ID, &c.Status, &c.Evidence, &c.Note, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CancelTestSession(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE test_sessions SET status='CANCELLED',finished_at=now()
 WHERE id=$1 AND status IN ('QUEUED','RUNNING')`, id)
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
	if _, err = tx.ExecContext(ctx, `UPDATE test_session_commands SET status='CANCELLED',finished_at=now()
 WHERE session_id=$1 AND status='PENDING'`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuthenticateTestSession(ctx context.Context, id, token string) error {
	if len(token) < 32 {
		return testlab.ErrInvalid
	}
	var digest string
	if err := s.db.QueryRowContext(ctx, `SELECT agent_token_sha256 FROM test_sessions WHERE id=$1 AND status IN ('QUEUED','RUNNING')`, id).Scan(&digest); err != nil {
		return testlab.ErrInvalid
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(testlab.TokenDigest(token))) != 1 {
		return testlab.ErrInvalid
	}
	return nil
}

// ClaimTestSession leases one queued session for this machine. A machine that
// already holds a run or a session is busy; it is never pre-empted.
func (s *Store) ClaimTestSession(ctx context.Context, machineID string, environments ...string) (*testlab.SessionLease, error) {
	if len(environments) == 0 {
		environments = []string{""}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var platform, runID, sessionID string
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT platform,run_id,session_id,generation FROM desktop_test_machines
 WHERE id=$1 AND enabled=TRUE FOR UPDATE`, machineID).Scan(&platform, &runID, &sessionID, &generation)
	if err != nil {
		return nil, err
	}
	if runID != "" {
		return nil, testlab.ErrState
	}
	if sessionID != "" {
		// A session that finished while the runner was offline must not pin the
		// machine forever. Only an expired lease on a terminal session is
		// reclaimed automatically; a live runner keeps its lease.
		var reclaimable bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM test_sessions
 WHERE id=$1 AND status IN ('PASS','FAIL','BLOCKED','CANCELLED'))
 AND COALESCE((SELECT lease_until FROM desktop_test_machines WHERE id=$2), to_timestamp(0)) < now()`,
			sessionID, machineID).Scan(&reclaimable); err != nil {
			return nil, err
		}
		if !reclaimable {
			return nil, testlab.ErrState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE desktop_test_machines SET session_id='',session_generation=0,lease_until=NULL WHERE id=$1`, machineID); err != nil {
			return nil, err
		}
	}
	var id string
	var specJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT id,spec FROM test_sessions
 WHERE status='QUEUED' AND spec->>'platform'=$1 AND (spec->>'environment'='' OR spec->>'environment'=ANY($2))
 ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, platform, environments).Scan(&id, &specJSON)
	if errors.Is(err, sql.ErrNoRows) {
		if _, uerr := tx.ExecContext(ctx, `UPDATE desktop_test_machines SET last_seen=now() WHERE id=$1`, machineID); uerr != nil {
			return nil, uerr
		}
		if cerr := tx.Commit(); cerr != nil {
			return nil, cerr
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var spec testlab.SessionSpec
	if err = json.Unmarshal(specJSON, &spec); err != nil {
		return nil, err
	}
	generation++
	var until, deadline time.Time
	err = tx.QueryRowContext(ctx, `UPDATE desktop_test_machines
 SET session_id=$2,session_generation=$3,lease_until=now()+interval '60 seconds',last_seen=now()
 WHERE id=$1 RETURNING lease_until`, machineID, id, generation).Scan(&until)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `UPDATE test_sessions SET status='RUNNING',machine_id=$2,lease_generation=$3,
 lease_until=now()+interval '60 seconds',started_at=now(),deadline=now()+($4::int * interval '1 second')
 WHERE id=$1 AND status='QUEUED' RETURNING deadline`, id, machineID, generation, spec.TimeoutSeconds).Scan(&deadline)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	session, err := s.GetTestSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &testlab.SessionLease{SessionID: id, MachineID: machineID, Generation: generation,
		ExpiresAt: until, Deadline: deadline, Session: session}, nil
}

func (s *Store) HeartbeatTestSession(ctx context.Context, machineID, sessionID string, generation int64) (time.Time, time.Time, error) {
	var until, deadline time.Time
	err := s.db.QueryRowContext(ctx, `UPDATE desktop_test_machines m SET last_seen=now(),lease_until=now()+interval '60 seconds'
 WHERE m.id=$1 AND m.session_id=$2 AND m.session_generation=$3 AND m.enabled=TRUE AND m.lease_until>now()
 AND EXISTS(SELECT 1 FROM test_sessions t WHERE t.id=$2 AND t.status='RUNNING' AND t.machine_id=$1 AND t.lease_generation=$3)
 RETURNING m.lease_until`, machineID, sessionID, generation).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return until, deadline, testlab.ErrState
	}
	if err != nil {
		return until, deadline, err
	}
	if err = s.db.QueryRowContext(ctx, `UPDATE test_sessions SET lease_until=now()+interval '60 seconds' WHERE id=$1 RETURNING deadline`, sessionID).Scan(&deadline); err != nil {
		return until, deadline, err
	}
	return until, deadline, nil
}

// TakeTestSessionCommand claims the oldest pending command or returns
// ErrNotFound so the handler can wait. ErrSessionTerminal means the runner must
// stop polling even if commands are still queued.
func (s *Store) TakeTestSessionCommand(ctx context.Context, machineID, sessionID string, generation int64) (*testlab.SessionCommand, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM test_sessions
 WHERE id=$1 AND machine_id=$2 AND lease_generation=$3 FOR UPDATE`, sessionID, machineID, generation).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, testlab.ErrState
	}
	if err != nil {
		return nil, err
	}
	if status != testlab.SessionRunning {
		return nil, ErrSessionTerminal
	}
	var c testlab.SessionCommand
	err = tx.QueryRowContext(ctx, `WITH next AS (
 SELECT id FROM test_session_commands WHERE session_id=$1 AND status='PENDING'
 ORDER BY seq LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE test_session_commands c SET status='RUNNING',started_at=now()
 FROM next WHERE c.id=next.id
 RETURNING c.id,c.seq,c.kind,c.args,c.status,c.created_at,c.started_at`, sessionID).Scan(
		&c.ID, &c.Seq, &c.Kind, &c.Args, &c.Status, &c.CreatedAt, &c.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) CompleteTestSessionCommand(ctx context.Context, machineID, sessionID string, generation int64, commandID, status string, result json.RawMessage) error {
	if status != "DONE" && status != "FAILED" && status != "CANCELLED" {
		return testlab.ErrInvalid
	}
	if len(result) > 1<<20 {
		return testlab.ErrInvalid
	}
	var id string
	err := s.db.QueryRowContext(ctx, `UPDATE test_session_commands c SET status=$4,result=$5,finished_at=now()
 WHERE c.id=$1 AND c.session_id=$2 AND c.status='RUNNING'
 AND EXISTS(SELECT 1 FROM test_sessions t WHERE t.id=$2 AND t.machine_id=$3 AND t.lease_generation=$6 AND t.status='RUNNING')
 RETURNING c.id`, commandID, sessionID, machineID, status, result, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return testlab.ErrState
	}
	return err
}

func (s *Store) EnqueueTestSessionCommand(ctx context.Context, sessionID, kind string, args json.RawMessage) (*testlab.SessionCommand, error) {
	if err := testlab.ValidateSessionCommand(kind, args); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM test_sessions WHERE id=$1`, sessionID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if status != testlab.SessionRunning {
		return nil, testlab.ErrState
	}
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM test_session_commands WHERE session_id=$1 AND status IN ('PENDING','RUNNING')`, sessionID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending >= 32 {
		return nil, fmt.Errorf("%w: too many outstanding commands", testlab.ErrState)
	}
	c := testlab.SessionCommand{ID: newID("cmd"), Kind: kind, Args: args, Status: "PENDING"}
	if err := s.db.QueryRowContext(ctx, `INSERT INTO test_session_commands(id,session_id,kind,args,status)
 VALUES($1,$2,$3,$4,'PENDING') RETURNING seq,created_at`, c.ID, sessionID, kind, []byte(args)).Scan(&c.Seq, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetTestSessionCommand(ctx context.Context, sessionID, commandID string) (*testlab.SessionCommand, error) {
	var c testlab.SessionCommand
	var result []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,seq,kind,args,status,result,created_at,started_at,finished_at
 FROM test_session_commands WHERE id=$1 AND session_id=$2`, commandID, sessionID).Scan(
		&c.ID, &c.Seq, &c.Kind, &c.Args, &c.Status, &result, &c.CreatedAt, &c.StartedAt, &c.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if result != nil {
		c.Result = result
	}
	return &c, nil
}

func (s *Store) ListTestSessionCommands(ctx context.Context, sessionID string) ([]testlab.SessionCommand, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,seq,kind,args,status,result,created_at,started_at,finished_at
 FROM test_session_commands WHERE session_id=$1 ORDER BY seq DESC LIMIT 200`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []testlab.SessionCommand{}
	for rows.Next() {
		var c testlab.SessionCommand
		var result []byte
		if err = rows.Scan(&c.ID, &c.Seq, &c.Kind, &c.Args, &c.Status, &result, &c.CreatedAt, &c.StartedAt, &c.FinishedAt); err != nil {
			return nil, err
		}
		if result != nil {
			c.Result = result
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RecordTestSessionCheck(ctx context.Context, sessionID string, check testlab.SessionCheck) error {
	if check.ID == "" || strings.TrimSpace(check.Status) == "" {
		return testlab.ErrInvalid
	}
	switch check.Status {
	case "PASS", "FAIL", "BLOCKED", "SKIPPED":
	default:
		return testlab.ErrInvalid
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM test_sessions WHERE id=$1`, sessionID).Scan(&status); err != nil {
		return err
	}
	if status != testlab.SessionRunning {
		return testlab.ErrState
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM test_session_checks WHERE session_id=$1 AND check_id<>$2`, sessionID, check.ID).Scan(&count); err != nil {
		return err
	}
	if count >= testlab.MaxSessionChecks {
		return testlab.ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO test_session_checks(session_id,check_id,status,evidence,note,updated_at)
 VALUES($1,$2,$3,$4,$5,now())
 ON CONFLICT(session_id,check_id) DO UPDATE SET status=EXCLUDED.status,evidence=EXCLUDED.evidence,note=EXCLUDED.note,updated_at=now()`,
		sessionID, check.ID, check.Status, check.Evidence, check.Note)
	return err
}

// RequestTestSessionFinish records the agent's verdict and wakes the runner. The
// runner still performs cleanup and submits the terminal report.
func (s *Store) RequestTestSessionFinish(ctx context.Context, sessionID, verdict, summary string) error {
	if len(summary) > 4000 {
		return testlab.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE test_sessions SET finish_requested=TRUE,agent_verdict=$2,summary=$3
 WHERE id=$1 AND status='RUNNING'`, sessionID, verdict, summary)
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
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM test_session_commands WHERE session_id=$1 AND kind='finish' AND status IN ('PENDING','RUNNING')`, sessionID).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO test_session_commands(id,session_id,kind,args,status) VALUES($1,$2,'finish','{}','PENDING')`,
			newID("cmd"), sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) FinishTestSession(ctx context.Context, machineID, sessionID string, generation int64, requested, summary string) (*testlab.Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM desktop_test_machines
 WHERE id=$1 AND session_id=$2 AND session_generation=$3 AND enabled=TRUE AND lease_until>now() FOR UPDATE`,
		machineID, sessionID, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, testlab.ErrState
	}
	if err != nil {
		return nil, err
	}
	var specJSON []byte
	var storedVerdict string
	err = tx.QueryRowContext(ctx, `SELECT spec,agent_verdict FROM test_sessions
 WHERE id=$1 AND machine_id=$2 AND lease_generation=$3 AND status='RUNNING' FOR UPDATE`, sessionID, machineID, generation).Scan(&specJSON, &storedVerdict)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, testlab.ErrState
	}
	if err != nil {
		return nil, err
	}
	var spec testlab.SessionSpec
	if err = json.Unmarshal(specJSON, &spec); err != nil {
		return nil, err
	}
	if requested == "" {
		requested = storedVerdict
	}
	checks, err := s.ListTestSessionChecks(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	verdict, err := testlab.EvaluateSession(spec, requested, checks)
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		if c.Evidence == "" {
			continue
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM test_session_evidence WHERE session_id=$1 AND ('sha256:'||sha256)=$2)`,
			sessionID, c.Evidence).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("%w: check %s references unknown evidence", testlab.ErrInvalid, c.ID)
		}
	}
	if len(summary) > 4000 {
		summary = summary[:4000]
	}
	// An empty runner summary keeps whatever the agent sent with finish.
	if _, err = tx.ExecContext(ctx, `UPDATE test_sessions SET status=$2,verdict=$2,
 summary=CASE WHEN $3='' THEN summary ELSE $3 END,finished_at=now()
 WHERE id=$1 AND status='RUNNING'`, sessionID, verdict, summary); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE test_session_commands SET status='CANCELLED',finished_at=now()
 WHERE session_id=$1 AND status='PENDING' AND kind<>'finish'`, sessionID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTestSession(ctx, sessionID)
}

// ReleaseTestSession acknowledges that local cleanup finished. Without it the
// machine stays busy even after a terminal session, matching run semantics.
func (s *Store) ReleaseTestSession(ctx context.Context, machineID, sessionID string, generation int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_test_machines m SET session_id='',session_generation=0,lease_until=NULL,last_seen=now()
 WHERE m.id=$1 AND m.session_id=$2 AND m.session_generation=$3
 AND EXISTS(SELECT 1 FROM test_sessions t WHERE t.id=$2 AND t.status IN ('PASS','FAIL','BLOCKED','CANCELLED'))`,
		machineID, sessionID, generation)
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
