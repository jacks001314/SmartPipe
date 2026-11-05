package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// IssuePlan 是复杂 Issue 的方案版本。方案内容不可变：批准时用 content_hash 校验。
type IssuePlan struct {
	ID          int64     `json:"id"`
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	Version     int       `json:"version"`
	Body        string    `json:"body"`
	ContentHash string    `json:"content_hash"`
	Author      string    `json:"author"`
	CommentID   int64     `json:"comment_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// IssueApproval 是一次方案批准。
type IssueApproval struct {
	ID          int64     `json:"id"`
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	Version     int       `json:"version"`
	PlanHash    string    `json:"plan_hash"`
	Approver    string    `json:"approver"`
	CommentID   int64     `json:"comment_id"`
	ApprovedAt  time.Time `json:"approved_at"`
}

const issuePlanColumns = `id, repo, issue_number, version, body, content_hash, author, comment_id, created_at`

func scanIssuePlan(row interface{ Scan(...any) error }) (*IssuePlan, error) {
	var p IssuePlan
	err := row.Scan(&p.ID, &p.Repo, &p.IssueNumber, &p.Version, &p.Body, &p.ContentHash, &p.Author, &p.CommentID, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePlan 追加一个方案版本（版本号按 repo+issue 递增）。
func (s *Store) CreatePlan(ctx context.Context, repo string, issueNumber int, body, hash, author string, commentID int64) (*IssuePlan, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO issue_plans (repo, issue_number, version, body, content_hash, author, comment_id)
VALUES ($1, $2, COALESCE((SELECT max(version) FROM issue_plans WHERE repo = $1 AND issue_number = $2), 0) + 1, $3, $4, $5, $6)
RETURNING `+issuePlanColumns,
		repo, issueNumber, body, hash, author, commentID)
	p, err := scanIssuePlan(row)
	if err != nil {
		return nil, fmt.Errorf("store: create plan: %w", err)
	}
	return p, nil
}

// GetPlan 读取指定版本；不存在返回 ErrNotFound。
func (s *Store) GetPlan(ctx context.Context, repo string, issueNumber, version int) (*IssuePlan, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+issuePlanColumns+`
  FROM issue_plans WHERE repo = $1 AND issue_number = $2 AND version = $3`, repo, issueNumber, version)
	p, err := scanIssuePlan(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get plan: %w", err)
	}
	return p, nil
}

// LatestPlan 读取最新方案版本。
func (s *Store) LatestPlan(ctx context.Context, repo string, issueNumber int) (*IssuePlan, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+issuePlanColumns+`
  FROM issue_plans WHERE repo = $1 AND issue_number = $2 ORDER BY version DESC LIMIT 1`, repo, issueNumber)
	p, err := scanIssuePlan(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: latest plan: %w", err)
	}
	return p, nil
}

// ApprovePlan 记录批准；同一版本重复批准返回 ErrDuplicate。
func (s *Store) ApprovePlan(ctx context.Context, a IssueApproval) (*IssueApproval, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO issue_approvals (repo, issue_number, version, plan_hash, approver, comment_id)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id, repo, issue_number, version, plan_hash, approver, comment_id, approved_at`,
		a.Repo, a.IssueNumber, a.Version, a.PlanHash, a.Approver, a.CommentID)
	var out IssueApproval
	if err := row.Scan(&out.ID, &out.Repo, &out.IssueNumber, &out.Version, &out.PlanHash, &out.Approver, &out.CommentID, &out.ApprovedAt); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, fmt.Errorf("store: approve plan: %w", err)
	}
	return &out, nil
}

// GetApproval 读取某版本的批准记录。
func (s *Store) GetApproval(ctx context.Context, repo string, issueNumber, version int) (*IssueApproval, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, repo, issue_number, version, plan_hash, approver, comment_id, approved_at
  FROM issue_approvals WHERE repo = $1 AND issue_number = $2 AND version = $3`, repo, issueNumber, version)
	var a IssueApproval
	err := row.Scan(&a.ID, &a.Repo, &a.IssueNumber, &a.Version, &a.PlanHash, &a.Approver, &a.CommentID, &a.ApprovedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get approval: %w", err)
	}
	return &a, nil
}

// ListPlans 返回某 Issue 的全部方案版本。
func (s *Store) ListPlans(ctx context.Context, repo string, issueNumber int) ([]IssuePlan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+issuePlanColumns+`
  FROM issue_plans WHERE repo = $1 AND issue_number = $2 ORDER BY version ASC`, repo, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("store: list plans: %w", err)
	}
	defer rows.Close()
	out := make([]IssuePlan, 0, 4)
	for rows.Next() {
		p, err := scanIssuePlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PlanView 是方案 + 批准状态的只读视图。
type PlanView struct {
	IssuePlan
	Approved   bool       `json:"approved"`
	Approver   string     `json:"approver"`
	ApprovedAt *time.Time `json:"approved_at"`
}

// ListRecentPlans 返回最近的方案版本及其批准状态。
func (s *Store) ListRecentPlans(ctx context.Context, limit int) ([]PlanView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.repo, p.issue_number, p.version, p.body, p.content_hash, p.author, p.comment_id, p.created_at,
       a.approver, a.approved_at
  FROM issue_plans p
  LEFT JOIN issue_approvals a
    ON a.repo = p.repo AND a.issue_number = p.issue_number AND a.version = p.version
 ORDER BY p.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list recent plans: %w", err)
	}
	defer rows.Close()
	out := make([]PlanView, 0, limit)
	for rows.Next() {
		var (
			v        PlanView
			approver sql.NullString
			at       sql.NullTime
		)
		if err := rows.Scan(&v.ID, &v.Repo, &v.IssueNumber, &v.Version, &v.Body, &v.ContentHash, &v.Author, &v.CommentID, &v.CreatedAt, &approver, &at); err != nil {
			return nil, err
		}
		if approver.Valid {
			v.Approved = true
			v.Approver = approver.String
		}
		if at.Valid {
			t := at.Time
			v.ApprovedAt = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
