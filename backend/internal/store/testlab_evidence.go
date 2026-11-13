package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"smartpipe/backend/internal/testlab"
)

const MaxDesktopEvidenceBytes = 8 << 20

func init() {
	migrations = append(migrations, migration{version: 46, name: "desktop_test_evidence", sql: `
CREATE TABLE desktop_test_evidence (
 run_id TEXT NOT NULL REFERENCES desktop_test_runs(id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL,
 mime_type TEXT NOT NULL,
 payload BYTEA NOT NULL CHECK(octet_length(payload) <= 8388608),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,sha256)
);`})
}

type DesktopEvidence struct {
	SHA256   string `json:"sha256"`
	MIMEType string `json:"mime_type"`
	Payload  []byte `json:"payload"`
}

func ValidateDesktopEvidence(e DesktopEvidence) error {
	if len(e.Payload) == 0 || len(e.Payload) > MaxDesktopEvidenceBytes {
		return fmt.Errorf("%w: evidence size", testlab.ErrInvalid)
	}
	switch e.MIMEType {
	case "image/png", "image/jpeg", "application/json", "text/plain":
	default:
		return fmt.Errorf("%w: evidence MIME type", testlab.ErrInvalid)
	}
	digest := sha256.Sum256(e.Payload)
	if hex.EncodeToString(digest[:]) != e.SHA256 {
		return fmt.Errorf("%w: evidence hash mismatch", testlab.ErrInvalid)
	}
	return nil
}

// Upload is bounded and content-addressed. Locking the run serializes quota
// accounting with other uploads and completion. The row survives machine release.
func (s *Store) PutDesktopEvidence(ctx context.Context, machineID, runID string, generation int64, e DesktopEvidence) (string, error) {
	if err := ValidateDesktopEvidence(e); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM desktop_test_machines WHERE id=$1 AND run_id=$2 AND generation=$3 AND enabled=TRUE AND lease_until>now() FOR UPDATE`, machineID, runID, generation).Scan(&id)
	if err == sql.ErrNoRows {
		return "", testlab.ErrState
	}
	if err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM desktop_test_runs WHERE id=$1 AND machine_id=$2 AND lease_generation=$3 AND status='RUNNING' FOR UPDATE`, runID, machineID, generation).Scan(&id)
	if err == sql.ErrNoRows {
		return "", testlab.ErrState
	}
	if err != nil {
		return "", err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM desktop_test_evidence WHERE run_id=$1 AND sha256=$2)`, runID, e.SHA256).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		var total int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(octet_length(payload)),0) FROM desktop_test_evidence WHERE run_id=$1`, runID).Scan(&total); err != nil {
			return "", err
		}
		if total+int64(len(e.Payload)) > 64<<20 {
			return "", fmt.Errorf("%w: run evidence quota", testlab.ErrInvalid)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO desktop_test_evidence(run_id,sha256,mime_type,payload) VALUES($1,$2,$3,$4)`, runID, e.SHA256, e.MIMEType, e.Payload)
		if err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return "sha256:" + e.SHA256, nil
}
func (s *Store) GetDesktopEvidence(ctx context.Context, runID, digest string) (*DesktopEvidence, error) {
	var e DesktopEvidence
	err := s.db.QueryRowContext(ctx, `SELECT sha256,mime_type,payload FROM desktop_test_evidence WHERE run_id=$1 AND sha256=$2`, runID, digest).Scan(&e.SHA256, &e.MIMEType, &e.Payload)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = ValidateDesktopEvidence(e); err != nil {
		return nil, err
	}
	return &e, nil
}
func validateReportEvidence(ctx context.Context, tx *sql.Tx, runID string, report testlab.Report) error {
	for _, check := range report.Checks {
		if check.Status != "PASS" {
			continue
		}
		var exists bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM desktop_test_evidence WHERE run_id=$1 AND 'sha256:'||sha256=$2)`, runID, check.Evidence).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: missing uploaded evidence for %s", testlab.ErrInvalid, check.ID)
		}
	}
	return nil
}
