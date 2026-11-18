package store

import (
	"context"
	"database/sql"
	"errors"
	"smartpipe/backend/internal/testlab"
)

const MaxTestSessionEvidenceTotal = 64 << 20

func (s *Store) putTestSessionEvidence(ctx context.Context, sessionID string, e DesktopEvidence) (string, error) {
	if err := ValidateDesktopEvidence(e); err != nil {
		return "", err
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(octet_length(payload)),0) FROM test_session_evidence WHERE session_id=$1 AND sha256<>$2`,
		sessionID, e.SHA256).Scan(&total); err != nil {
		return "", err
	}
	if total+int64(len(e.Payload)) > MaxTestSessionEvidenceTotal {
		return "", testlab.ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO test_session_evidence(session_id,sha256,mime_type,payload)
 VALUES($1,$2,$3,$4) ON CONFLICT(session_id,sha256) DO NOTHING`, sessionID, e.SHA256, e.MIMEType, e.Payload)
	if err != nil {
		return "", err
	}
	return "sha256:" + e.SHA256, nil
}

// PutTestSessionMachineEvidence accepts evidence from the leased runner.
func (s *Store) PutTestSessionMachineEvidence(ctx context.Context, machineID, sessionID string, generation int64, e DesktopEvidence) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM test_sessions
 WHERE id=$1 AND machine_id=$2 AND lease_generation=$3 AND status='RUNNING'
 AND EXISTS(SELECT 1 FROM desktop_test_machines m WHERE m.id=$2 AND m.session_id=$1 AND m.session_generation=$3 AND m.lease_until>now())`,
		sessionID, machineID, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", testlab.ErrState
	}
	if err != nil {
		return "", err
	}
	return s.putTestSessionEvidence(ctx, sessionID, e)
}

// PutTestSessionAgentEvidence accepts evidence from the session-scoped agent
// token while the session is running.
func (s *Store) PutTestSessionAgentEvidence(ctx context.Context, sessionID string, e DesktopEvidence) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM test_sessions WHERE id=$1 AND status='RUNNING'`, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", testlab.ErrState
	}
	if err != nil {
		return "", err
	}
	return s.putTestSessionEvidence(ctx, sessionID, e)
}

func (s *Store) GetTestSessionEvidence(ctx context.Context, sessionID, sha string) (*DesktopEvidence, error) {
	var e DesktopEvidence
	err := s.db.QueryRowContext(ctx, `SELECT sha256,mime_type,payload FROM test_session_evidence WHERE session_id=$1 AND sha256=$2`,
		sessionID, sha).Scan(&e.SHA256, &e.MIMEType, &e.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}
