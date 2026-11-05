package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Derivation 记录一次由 lead 发起的子 Agent 派生。
type Derivation struct {
	ID              int64     `json:"id"`
	ProjectID       *string   `json:"project_id"`
	IssueRef        string    `json:"issue_ref"`
	AgentName       string    `json:"agent_name"`
	AgentID         string    `json:"agent_id"`
	OperationID     string    `json:"operation_id"`
	ApprovalVersion int       `json:"approval_version"`
	ApprovalHash    string    `json:"approval_hash"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// CreateDerivation 写入派生记录。
func (s *Store) CreateDerivation(ctx context.Context, d Derivation) (*Derivation, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO derivations (project_id, issue_ref, agent_name, agent_id, operation_id, approval_version, approval_hash, status)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING id, created_at`,
		d.ProjectID, d.IssueRef, d.AgentName, d.AgentID, d.OperationID, d.ApprovalVersion, d.ApprovalHash, d.Status)
	if err := row.Scan(&d.ID, &d.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: create derivation: %w", err)
	}
	return &d, nil
}

// ListDerivations 返回最近派生记录。
func (s *Store) ListDerivations(ctx context.Context, limit int) ([]Derivation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, issue_ref, agent_name, agent_id, operation_id, approval_version, approval_hash, status, created_at
  FROM derivations ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list derivations: %w", err)
	}
	defer rows.Close()
	out := make([]Derivation, 0, limit)
	for rows.Next() {
		var (
			d       Derivation
			project sql.NullString
		)
		if err := rows.Scan(&d.ID, &project, &d.IssueRef, &d.AgentName, &d.AgentID, &d.OperationID,
			&d.ApprovalVersion, &d.ApprovalHash, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		if project.Valid {
			v := project.String
			d.ProjectID = &v
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
