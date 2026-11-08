package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PgRepo 是 Postgres 实现，表结构见 store/migrate.go 的 scheduler_core 迁移。
type PgRepo struct {
	db *sql.DB
}

// NewPgRepo 用已有连接池构造仓储。
func NewPgRepo(db *sql.DB) *PgRepo { return &PgRepo{db: db} }

const workerColumns = `id, project_id, issue_number, issue_url, chat_id, state, branch, worktree,
    pr_number, attempts, last_error, created_at, updated_at`

func scanWorker(row interface{ Scan(...any) error }) (*Worker, error) {
	var w Worker
	var state string
	err := row.Scan(&w.ID, &w.ProjectID, &w.Issue, &w.IssueURL, &w.ChatID, &state, &w.Branch,
		&w.Worktree, &w.PRNumber, &w.Attempts, &w.LastError, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.State = State(state)
	return &w, nil
}

func (r *PgRepo) PutLink(ctx context.Context, l Link) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO scheduler_links (platform_message_id, project_id, issue_number, chat_id)
VALUES ($1,$2,$3,$4) ON CONFLICT (platform_message_id) DO NOTHING`,
		l.PlatformMessageID, l.ProjectID, l.Issue, l.ChatID)
	if err != nil {
		return fmt.Errorf("scheduler: put link: %w", err)
	}
	return nil
}

func (r *PgRepo) GetLink(ctx context.Context, msgID string) (*Link, error) {
	var l Link
	err := r.db.QueryRowContext(ctx, `SELECT platform_message_id, project_id, issue_number, chat_id, created_at
 FROM scheduler_links WHERE platform_message_id = $1`, msgID).
		Scan(&l.PlatformMessageID, &l.ProjectID, &l.Issue, &l.ChatID, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *PgRepo) AppendChat(ctx context.Context, m ChatMessage) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO scheduler_chat (chat_id, project_id, role, body) VALUES ($1,$2,$3,$4)`,
		m.ChatID, m.ProjectID, m.Role, m.Body)
	if err != nil {
		return fmt.Errorf("scheduler: append chat: %w", err)
	}
	return nil
}

func (r *PgRepo) RecentChat(ctx context.Context, chatID string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, chat_id, project_id, role, body, created_at
 FROM scheduler_chat WHERE chat_id = $1 ORDER BY id DESC LIMIT $2`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.ProjectID, &m.Role, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	// reverse to chronological
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (r *PgRepo) CreateWorker(ctx context.Context, w *Worker) (bool, error) {
	if w.State == "" {
		w.State = StateQueued
	}
	if w.ID == "" {
		w.ID = fmt.Sprintf("wk-%d", time.Now().UnixNano())
	}
	row := r.db.QueryRowContext(ctx, `
INSERT INTO scheduler_workers (id, project_id, issue_number, issue_url, chat_id, state, branch, worktree)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (project_id, issue_number) WHERE state NOT IN ('closed','failed') DO NOTHING
RETURNING `+workerColumns,
		w.ID, w.ProjectID, w.Issue, w.IssueURL, w.ChatID, string(w.State), w.Branch, w.Worktree)
	got, err := scanWorker(row)
	if err != nil {
		return false, err
	}
	if got == nil {
		return false, nil
	}
	*w = *got
	return true, nil
}

func (r *PgRepo) GetWorker(ctx context.Context, id string) (*Worker, error) {
	return scanWorker(r.db.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM scheduler_workers WHERE id = $1`, id))
}

func (r *PgRepo) ActiveWorker(ctx context.Context, projectID string, issue int) (*Worker, error) {
	return scanWorker(r.db.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM scheduler_workers
 WHERE project_id = $1 AND issue_number = $2 AND state NOT IN ('closed','failed')
 ORDER BY updated_at DESC LIMIT 1`, projectID, issue))
}

func (r *PgRepo) UpdateWorker(ctx context.Context, w *Worker) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduler_workers SET
 state=$2, branch=$3, worktree=$4, pr_number=$5, attempts=$6, last_error=$7, updated_at=now()
 WHERE id=$1`,
		w.ID, string(w.State), w.Branch, w.Worktree, w.PRNumber, w.Attempts, w.LastError)
	if err != nil {
		return fmt.Errorf("scheduler: update worker: %w", err)
	}
	return nil
}

func (r *PgRepo) ListWorkers(ctx context.Context) ([]*Worker, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+workerColumns+` FROM scheduler_workers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *PgRepo) ClaimNext(ctx context.Context, maxGlobal, maxPerProject int) (*Worker, error) {
	if maxGlobal <= 0 {
		maxGlobal = 1 << 20
	}
	if maxPerProject <= 0 {
		maxPerProject = 1 << 20
	}
	const active = `('preparing','coding','verifying','pushing')`
	query := `UPDATE scheduler_workers SET state='preparing', updated_at=now()
WHERE id = (
  SELECT w.id FROM scheduler_workers w
  WHERE w.state='queued'
    AND (SELECT count(*) FROM scheduler_workers a WHERE a.state IN ` + active + `) < $1
    AND (SELECT count(*) FROM scheduler_workers a WHERE a.project_id=w.project_id AND a.state IN ` + active + `) < $2
  ORDER BY w.created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
) RETURNING ` + workerColumns
	return scanWorker(r.db.QueryRowContext(ctx, query, maxGlobal, maxPerProject))
}
