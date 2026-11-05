package store

import (
	"context"
	"fmt"
	"time"
)

// RepoOp 是一次提交或推送的操作记录（lead 的合并证据）。
type RepoOp struct {
	ID           int64     `json:"id"`
	ProjectID    *string   `json:"project_id"`
	Kind         string    `json:"kind"`
	IssueRef     string    `json:"issue_ref"`
	Repo         string    `json:"repo"`
	Branch       string    `json:"branch"`
	TargetBranch string    `json:"target_branch"`
	Commit       string    `json:"commit"`
	OK           bool      `json:"ok"`
	Error        string    `json:"error"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateRepoOp 记录一次提交/推送。
func (s *Store) CreateRepoOp(ctx context.Context, op RepoOp) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO repo_ops (project_id, kind, issue_ref, repo, branch, target_branch, commit_sha, ok, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		op.ProjectID, op.Kind, op.IssueRef, op.Repo, op.Branch, op.TargetBranch, op.Commit, op.OK, truncate(op.Error, 2000))
	if err != nil {
		return fmt.Errorf("store: create repo op: %w", err)
	}
	return nil
}

// ListRepoOps 返回最近的提交/推送记录。
func (s *Store) ListRepoOps(ctx context.Context, limit int) ([]RepoOp, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, kind, issue_ref, repo, branch, target_branch, commit_sha, ok, error, created_at
FROM repo_ops ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list repo ops: %w", err)
	}
	defer rows.Close()
	out := make([]RepoOp, 0, limit)
	for rows.Next() {
		var (
			op  RepoOp
			pid *string
		)
		if err := rows.Scan(&op.ID, &pid, &op.Kind, &op.IssueRef, &op.Repo, &op.Branch, &op.TargetBranch, &op.Commit, &op.OK, &op.Error, &op.CreatedAt); err != nil {
			return nil, err
		}
		op.ProjectID = pid
		out = append(out, op)
	}
	return out, rows.Err()
}

func truncate(v string, n int) string {
	if n <= 0 || len(v) <= n {
		return v
	}
	return v[:n]
}
