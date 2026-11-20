package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"smartpipe/backend/internal/testlab"
)

// MaxTestSessionInputBytes bounds one staged engine input. Inputs are produced
// by the SmartPipe scheduler, not by the runner, and travel through PostgreSQL
// so a single blob must stay well below the connection limits.
const MaxTestSessionInputBytes = 256 << 20

var sessionInputName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var sessionInputDigest = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
var sessionInputCheckID = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,80}$`)

// DesktopSessionInput is an immutable blob the scheduler staged for one engine
// session. The engine downloads it by name and verifies SHA256 before unpacking.
type DesktopSessionInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	SHA256   string `json:"sha256"`
	MIMEType string `json:"mime_type"`
	Bytes    int64  `json:"bytes"`
	Payload  []byte `json:"-"`
}

func init() {
	migrations = append(migrations, migration{version: 58, name: "test_session_engine_inputs", sql: `
CREATE TABLE IF NOT EXISTS test_session_inputs (
 session_id TEXT NOT NULL REFERENCES test_sessions(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT '',
 sha256 TEXT NOT NULL,
 mime_type TEXT NOT NULL DEFAULT 'application/gzip',
 bytes BIGINT NOT NULL,
 payload BYTEA NOT NULL CHECK (octet_length(payload) <= 268435456),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id,name)
);
CREATE TABLE IF NOT EXISTS test_session_findings (
 session_id TEXT NOT NULL REFERENCES test_sessions(id) ON DELETE CASCADE,
 finding_id TEXT NOT NULL,
 severity TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'open',
 title TEXT NOT NULL,
 detail TEXT NOT NULL DEFAULT '',
 repro TEXT NOT NULL DEFAULT '',
 expected TEXT NOT NULL DEFAULT '',
 actual TEXT NOT NULL DEFAULT '',
 evidence JSONB NOT NULL DEFAULT '[]',
 check_ref TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id,finding_id)
);
`})
}

// PutTestSessionInput records one staged input. It is idempotent while the
// session is still queued and the content hash matches the declaration.
func (s *Store) PutTestSessionInput(ctx context.Context, sessionID string, in DesktopSessionInput) error {
	if !sessionInputName.MatchString(in.Name) || len(in.Payload) == 0 || in.Bytes != int64(len(in.Payload)) ||
		len(in.Payload) > MaxTestSessionInputBytes || !sessionInputDigest.MatchString(in.SHA256) {
		return testlab.ErrInvalid
	}
	if in.MIMEType == "" {
		in.MIMEType = "application/gzip"
	}
	var status string
	var specJSON []byte
	if err := s.db.QueryRowContext(ctx, `SELECT status,spec FROM test_sessions WHERE id=$1`, sessionID).Scan(&status, &specJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != testlab.SessionQueued && status != testlab.SessionRunning {
		return testlab.ErrState
	}
	var spec testlab.SessionSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return err
	}
	if spec.Engine == nil {
		return testlab.ErrInvalid
	}
	declared := false
	for _, candidate := range spec.Engine.Inputs {
		if candidate.Name != in.Name {
			continue
		}
		declared = true
		if !strings.EqualFold(candidate.SHA256, in.SHA256) {
			return fmt.Errorf("%w: input %s does not match the declared sha256", testlab.ErrInvalid, in.Name)
		}
		if candidate.Bytes > 0 && candidate.Bytes != in.Bytes {
			return fmt.Errorf("%w: input %s does not match the declared size", testlab.ErrInvalid, in.Name)
		}
	}
	if !declared {
		return fmt.Errorf("%w: input %s is not declared by the session", testlab.ErrInvalid, in.Name)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO test_session_inputs(session_id,name,kind,sha256,mime_type,bytes,payload)
 VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(session_id,name) DO UPDATE SET kind=EXCLUDED.kind,sha256=EXCLUDED.sha256,
  mime_type=EXCLUDED.mime_type,bytes=EXCLUDED.bytes,payload=EXCLUDED.payload,created_at=now()`,
		sessionID, in.Name, in.Kind, strings.ToLower(in.SHA256), in.MIMEType, in.Bytes, in.Payload)
	return err
}

func (s *Store) GetTestSessionInput(ctx context.Context, sessionID, name string) (*DesktopSessionInput, error) {
	var in DesktopSessionInput
	err := s.db.QueryRowContext(ctx, `SELECT name,kind,sha256,mime_type,bytes,payload
 FROM test_session_inputs WHERE session_id=$1 AND name=$2`, sessionID, name).Scan(
		&in.Name, &in.Kind, &in.SHA256, &in.MIMEType, &in.Bytes, &in.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

func (s *Store) ListTestSessionInputs(ctx context.Context, sessionID string) ([]DesktopSessionInput, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,kind,sha256,mime_type,bytes
 FROM test_session_inputs WHERE session_id=$1 ORDER BY name`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DesktopSessionInput{}
	for rows.Next() {
		var in DesktopSessionInput
		if err := rows.Scan(&in.Name, &in.Kind, &in.SHA256, &in.MIMEType, &in.Bytes); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// ListTestSessionMachineInputs returns the staged input manifest to the runner
// holding the live lease. It deliberately omits payloads.
func (s *Store) ListTestSessionMachineInputs(ctx context.Context, machineID, sessionID string, generation int64) ([]DesktopSessionInput, error) {
	if generation < 1 {
		return nil, testlab.ErrInvalid
	}
	if err := s.sessionLeaseActive(ctx, machineID, sessionID, generation); err != nil {
		return nil, err
	}
	return s.ListTestSessionInputs(ctx, sessionID)
}

func (s *Store) GetTestSessionMachineInput(ctx context.Context, machineID, sessionID string, generation int64, name string) (*DesktopSessionInput, error) {
	if generation < 1 {
		return nil, testlab.ErrInvalid
	}
	if err := s.sessionLeaseActive(ctx, machineID, sessionID, generation); err != nil {
		return nil, err
	}
	return s.GetTestSessionInput(ctx, sessionID, name)
}

func (s *Store) ListTestSessionFindings(ctx context.Context, sessionID string) ([]testlab.Finding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT finding_id,severity,status,title,detail,repro,expected,actual,evidence,check_ref
 FROM test_session_findings WHERE session_id=$1 ORDER BY finding_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []testlab.Finding
	for rows.Next() {
		var f testlab.Finding
		var evidence []byte
		if err := rows.Scan(&f.ID, &f.Severity, &f.Status, &f.Title, &f.Detail, &f.Repro, &f.Expected, &f.Actual, &evidence, &f.CheckRef); err != nil {
			return nil, err
		}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &f.Evidence); err != nil {
				return nil, err
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func recordSessionFindings(ctx context.Context, tx *sql.Tx, sessionID string, findings []testlab.Finding) error {
	if len(findings) > testlab.MaxSessionFindings {
		return testlab.ErrInvalid
	}
	if findings == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM test_session_findings WHERE session_id=$1`, sessionID); err != nil {
		return err
	}
	for _, f := range findings {
		if err := f.Validate(); err != nil {
			return err
		}
		if f.Status == "" {
			f.Status = "open"
		}
		evidence, err := json.Marshal(f.Evidence)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO test_session_findings(session_id,finding_id,severity,status,title,detail,repro,expected,actual,evidence,check_ref)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, sessionID, f.ID, f.Severity, f.Status, f.Title, f.Detail,
			f.Repro, f.Expected, f.Actual, evidence, f.CheckRef); err != nil {
			return err
		}
	}
	return nil
}

func recordSessionChecks(ctx context.Context, tx *sql.Tx, sessionID string, checks []testlab.SessionCheck) error {
	if checks == nil {
		return nil
	}
	if len(checks) > testlab.MaxSessionChecks {
		return testlab.ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM test_session_checks WHERE session_id=$1`, sessionID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if !sessionInputCheckID.MatchString(c.ID) || seen[c.ID] || len(c.Note) > 4000 {
			return testlab.ErrInvalid
		}
		seen[c.ID] = true
		switch c.Status {
		case "PASS", "FAIL", "BLOCKED", "SKIPPED":
		default:
			return testlab.ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO test_session_checks(session_id,check_id,status,evidence,note,updated_at)
 VALUES($1,$2,$3,$4,$5,now())`, sessionID, c.ID, c.Status, c.Evidence, c.Note); err != nil {
			return err
		}
	}
	return nil
}

func listSessionChecksTx(ctx context.Context, tx *sql.Tx, sessionID string) ([]testlab.SessionCheck, error) {
	rows, err := tx.QueryContext(ctx, `SELECT check_id,status,evidence,note,updated_at
 FROM test_session_checks WHERE session_id=$1 ORDER BY check_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []testlab.SessionCheck
	for rows.Next() {
		var c testlab.SessionCheck
		if err := rows.Scan(&c.ID, &c.Status, &c.Evidence, &c.Note, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func listSessionFindingsTx(ctx context.Context, tx *sql.Tx, sessionID string) ([]testlab.Finding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT finding_id,severity,status,title,detail,repro,expected,actual,evidence,check_ref
 FROM test_session_findings WHERE session_id=$1 ORDER BY finding_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []testlab.Finding
	for rows.Next() {
		var f testlab.Finding
		var evidence []byte
		if err := rows.Scan(&f.ID, &f.Severity, &f.Status, &f.Title, &f.Detail, &f.Repro, &f.Expected, &f.Actual, &evidence, &f.CheckRef); err != nil {
			return nil, err
		}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &f.Evidence); err != nil {
				return nil, err
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) sessionLeaseActive(ctx context.Context, machineID, sessionID string, generation int64) error {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM test_sessions
 WHERE id=$1 AND machine_id=$2 AND lease_generation=$3 AND status='RUNNING'
 AND EXISTS(SELECT 1 FROM desktop_test_machines m WHERE m.id=$2 AND m.session_id=$1 AND m.session_generation=$3 AND m.lease_until>now())`,
		sessionID, machineID, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return testlab.ErrState
	}
	return err
}

// UpsertTestSessionMachineCheck lets the engine publish a check while the
// session is still running, so reviewers see progress before the final report.
func (s *Store) UpsertTestSessionMachineCheck(ctx context.Context, machineID, sessionID string, generation int64, c testlab.SessionCheck) error {
	if !sessionInputCheckID.MatchString(c.ID) || len(c.Note) > 4000 || len(c.Evidence) > 200 {
		return testlab.ErrInvalid
	}
	switch c.Status {
	case "PASS", "FAIL", "BLOCKED", "SKIPPED":
	default:
		return testlab.ErrInvalid
	}
	if err := s.sessionLeaseActive(ctx, machineID, sessionID, generation); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO test_session_checks(session_id,check_id,status,evidence,note,updated_at)
 VALUES($1,$2,$3,$4,$5,now())
 ON CONFLICT(session_id,check_id) DO UPDATE SET status=EXCLUDED.status,evidence=EXCLUDED.evidence,note=EXCLUDED.note,updated_at=now()`,
		sessionID, c.ID, c.Status, c.Evidence, c.Note)
	return err
}

// UpsertTestSessionMachineFinding publishes one finding before the report.
func (s *Store) UpsertTestSessionMachineFinding(ctx context.Context, machineID, sessionID string, generation int64, f testlab.Finding) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.Status == "" {
		f.Status = "open"
	}
	if err := s.sessionLeaseActive(ctx, machineID, sessionID, generation); err != nil {
		return err
	}
	evidence, err := json.Marshal(f.Evidence)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO test_session_findings(session_id,finding_id,severity,status,title,detail,repro,expected,actual,evidence,check_ref)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
 ON CONFLICT(session_id,finding_id) DO UPDATE SET severity=EXCLUDED.severity,status=EXCLUDED.status,title=EXCLUDED.title,
  detail=EXCLUDED.detail,repro=EXCLUDED.repro,expected=EXCLUDED.expected,actual=EXCLUDED.actual,
  evidence=EXCLUDED.evidence,check_ref=EXCLUDED.check_ref,updated_at=now()`, sessionID, f.ID, f.Severity, f.Status,
		f.Title, f.Detail, f.Repro, f.Expected, f.Actual, evidence, f.CheckRef)
	return err
}
