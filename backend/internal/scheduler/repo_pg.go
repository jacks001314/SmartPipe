package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// PgRepo 是 Postgres 实现，表结构见 store/migrate.go 的 scheduler_core 迁移。
type PgRepo struct {
	db *sql.DB
}

// NewPgRepo 用已有连接池构造仓储。
func NewPgRepo(db *sql.DB) *PgRepo { return &PgRepo{db: db} }

const workerColumns = `id, project_id, issue_number, issue_title, issue_url, chat_id, state, branch, worktree,
    pr_number, pr_url, attempts, last_error, blocked_reason, priority, queued_at, started_at,
    state_changed_at, heartbeat_at, finished_at, round, model, provider_id, created_at, updated_at`

func scanWorker(row interface{ Scan(...any) error }) (*Worker, error) {
	var (
		w         Worker
		state     string
		started   sql.NullTime
		heartbeat sql.NullTime
		finished  sql.NullTime
	)
	err := row.Scan(&w.ID, &w.ProjectID, &w.Issue, &w.IssueTitle, &w.IssueURL, &w.ChatID, &state, &w.Branch,
		&w.Worktree, &w.PRNumber, &w.PRURL, &w.Attempts, &w.LastError, &w.BlockedReason, &w.Priority,
		&w.QueuedAt, &started, &w.StateChangedAt, &heartbeat, &finished, &w.Round, &w.Model, &w.ProviderID,
		&w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.State = State(state)
	if started.Valid {
		v := started.Time
		w.StartedAt = &v
	}
	if heartbeat.Valid {
		v := heartbeat.Time
		w.HeartbeatAt = &v
	}
	if finished.Valid {
		v := finished.Time
		w.FinishedAt = &v
	}
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

func (r *PgRepo) RecordMessage(ctx context.Context, m MessageRecord) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO lanxin_messages
 (platform_message_id, bot_id, bot_name, chat_type, chat_id, user_id, sender_name, group_name,
  msg_type, text, is_at_me, project_id, status, reason, issue_number, issue_url)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),$13,$14,$15,$16)
ON CONFLICT (platform_message_id) DO UPDATE SET
 bot_id=EXCLUDED.bot_id, bot_name=EXCLUDED.bot_name, chat_type=EXCLUDED.chat_type,
 chat_id=EXCLUDED.chat_id, user_id=EXCLUDED.user_id, sender_name=EXCLUDED.sender_name,
 group_name=EXCLUDED.group_name, msg_type=EXCLUDED.msg_type, text=EXCLUDED.text,
 is_at_me=EXCLUDED.is_at_me,
 project_id=COALESCE(NULLIF(EXCLUDED.project_id,''), lanxin_messages.project_id),
 status=EXCLUDED.status, reason=EXCLUDED.reason, issue_number=EXCLUDED.issue_number,
 issue_url=EXCLUDED.issue_url, updated_at=now()`,
		m.PlatformMessageID, m.BotID, m.BotName, m.ChatType, m.ChatID, m.SenderID, m.SenderName,
		m.GroupName, m.MsgType, m.Text, m.IsAtMe, m.ProjectID, m.Status, m.Reason, m.Issue, m.IssueURL)
	if err != nil {
		return fmt.Errorf("scheduler: record message: %w", err)
	}
	return nil
}

func (r *PgRepo) MarkMessage(ctx context.Context, platformMessageID, status, reason string, issue int, issueURL string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE lanxin_messages
 SET status=$2, reason=$3, issue_number=$4, issue_url=$5, updated_at=now()
 WHERE platform_message_id=$1`, platformMessageID, status, reason, issue, issueURL)
	if err != nil {
		return fmt.Errorf("scheduler: mark message: %w", err)
	}
	return nil
}

func (r *PgRepo) ListMessages(ctx context.Context, limit int) ([]MessageRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, platform_message_id, bot_id, bot_name, chat_type, chat_id,
 user_id, sender_name, group_name, msg_type, text, is_at_me, COALESCE(project_id,''), status, reason,
 issue_number, issue_url, created_at, updated_at
 FROM lanxin_messages ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MessageRecord, 0)
	for rows.Next() {
		var m MessageRecord
		if err := rows.Scan(&m.ID, &m.PlatformMessageID, &m.BotID, &m.BotName, &m.ChatType, &m.ChatID,
			&m.SenderID, &m.SenderName, &m.GroupName, &m.MsgType, &m.Text, &m.IsAtMe, &m.ProjectID,
			&m.Status, &m.Reason, &m.Issue, &m.IssueURL, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
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
	if w.QueuedAt.IsZero() {
		w.QueuedAt = time.Now()
	}
	if w.StateChangedAt.IsZero() {
		w.StateChangedAt = time.Now()
	}
	row := r.db.QueryRowContext(ctx, `
INSERT INTO scheduler_workers (id, project_id, issue_number, issue_title, issue_url, chat_id, state, branch, worktree, priority, queued_at, state_changed_at, model, provider_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (project_id, issue_number) WHERE state NOT IN ('closed','failed') DO NOTHING
RETURNING `+workerColumns,
		w.ID, w.ProjectID, w.Issue, w.IssueTitle, w.IssueURL, w.ChatID, string(w.State), w.Branch, w.Worktree,
		w.Priority, w.QueuedAt, w.StateChangedAt, w.Model, w.ProviderID)
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
	if w.QueuedAt.IsZero() {
		w.QueuedAt = time.Now()
	}
	if w.StateChangedAt.IsZero() {
		w.StateChangedAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `UPDATE scheduler_workers SET
 issue_title=$2, state=$3, branch=$4, worktree=$5, pr_number=$6, pr_url=$7, attempts=$8,
 last_error=$9, blocked_reason=$10, priority=$11, queued_at=$12, started_at=$13,
 state_changed_at=$14, heartbeat_at=$15, finished_at=$16, round=$17, model=$18,
 provider_id=$19, updated_at=now()
 WHERE id=$1`,
		w.ID, w.IssueTitle, string(w.State), w.Branch, w.Worktree, w.PRNumber, w.PRURL, w.Attempts,
		w.LastError, w.BlockedReason, w.Priority, w.QueuedAt, w.StartedAt, w.StateChangedAt,
		w.HeartbeatAt, w.FinishedAt, w.Round, w.Model, w.ProviderID)
	if err != nil {
		return fmt.Errorf("scheduler: update worker: %w", err)
	}
	return nil
}

func (r *PgRepo) TouchWorker(ctx context.Context, id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `UPDATE scheduler_workers SET heartbeat_at=$2, updated_at=$2 WHERE id=$1`, id, at)
	if err != nil {
		return fmt.Errorf("scheduler: touch worker: %w", err)
	}
	return nil
}

func (r *PgRepo) AppendWorkerEvent(ctx context.Context, ev WorkerEvent) error {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	detail, err := json.Marshal(ev.Detail)
	if err != nil {
		return fmt.Errorf("scheduler: event detail: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO scheduler_worker_events
 (worker_id, project_id, issue_number, at, kind, state_from, state_to, actor, summary, detail)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		ev.WorkerID, ev.ProjectID, ev.Issue, ev.At, ev.Kind, ev.StateFrom, ev.StateTo, ev.Actor, ev.Summary, detail)
	if err != nil {
		return fmt.Errorf("scheduler: append worker event: %w", err)
	}
	return nil
}

func (r *PgRepo) ListWorkerEvents(ctx context.Context, workerID string, limit int) ([]WorkerEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, worker_id, project_id, issue_number, at, kind,
 state_from, state_to, actor, summary, detail
 FROM scheduler_worker_events WHERE worker_id=$1 ORDER BY id DESC LIMIT $2`, workerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkerEvent, 0)
	for rows.Next() {
		var (
			ev     WorkerEvent
			detail []byte
		)
		if err := rows.Scan(&ev.ID, &ev.WorkerID, &ev.ProjectID, &ev.Issue, &ev.At, &ev.Kind,
			&ev.StateFrom, &ev.StateTo, &ev.Actor, &ev.Summary, &detail); err != nil {
			return nil, err
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &ev.Detail)
		}
		out = append(out, ev)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

const workerOutputColumns = `worker_id, item_id, round, turn_id, kind, status, title,
 content, output, metadata, bytes, truncated, blob_ref, revision, created_at, updated_at`

func scanWorkerOutput(row interface{ Scan(...any) error }) (*WorkerOutput, error) {
	var out WorkerOutput
	var metadata []byte
	err := row.Scan(&out.WorkerID, &out.ItemID, &out.Round, &out.TurnID, &out.Kind, &out.Status,
		&out.Title, &out.Content, &out.Output, &metadata, &out.Bytes, &out.Truncated, &out.BlobRef,
		&out.Revision, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(metadata) > 0 {
		out.Metadata = append(json.RawMessage(nil), metadata...)
	}
	return &out, nil
}

func (r *PgRepo) UpsertWorkerOutput(ctx context.Context, out *WorkerOutput) error {
	if out == nil || out.WorkerID == "" || out.ItemID == "" {
		return nil
	}
	if len(out.Metadata) == 0 {
		out.Metadata = json.RawMessage(`{}`)
	}
	row := r.db.QueryRowContext(ctx, `
INSERT INTO scheduler_worker_outputs
 (worker_id, item_id, round, turn_id, kind, status, title, content, output, metadata,
  bytes, truncated, blob_ref, revision, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,nextval('scheduler_worker_output_revision_seq'),now(),now())
ON CONFLICT (worker_id, item_id) DO UPDATE SET
 round=EXCLUDED.round, turn_id=EXCLUDED.turn_id, kind=EXCLUDED.kind, status=EXCLUDED.status,
 title=EXCLUDED.title, content=EXCLUDED.content, output=EXCLUDED.output, metadata=EXCLUDED.metadata,
 bytes=EXCLUDED.bytes, truncated=EXCLUDED.truncated, blob_ref=EXCLUDED.blob_ref,
 revision=EXCLUDED.revision, updated_at=now()
RETURNING `+workerOutputColumns,
		out.WorkerID, out.ItemID, out.Round, out.TurnID, out.Kind, out.Status, out.Title,
		out.Content, out.Output, []byte(out.Metadata), out.Bytes, out.Truncated, out.BlobRef)
	got, err := scanWorkerOutput(row)
	if err != nil {
		return fmt.Errorf("scheduler: upsert worker output: %w", err)
	}
	if got != nil {
		*out = *got
	}
	return nil
}

func (r *PgRepo) ListWorkerOutputs(ctx context.Context, workerID string, afterRevision int64, limit int) ([]WorkerOutput, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+workerOutputColumns+` FROM scheduler_worker_outputs
 WHERE worker_id=$1 AND revision>$2 ORDER BY revision ASC LIMIT $3`, workerID, afterRevision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkerOutput, 0)
	for rows.Next() {
		item, err := scanWorkerOutput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (r *PgRepo) GetWorkerOutput(ctx context.Context, workerID, itemID string) (*WorkerOutput, error) {
	return scanWorkerOutput(r.db.QueryRowContext(ctx, `SELECT `+workerOutputColumns+` FROM scheduler_worker_outputs
 WHERE worker_id=$1 AND item_id=$2`, workerID, itemID))
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
  ORDER BY w.priority DESC, w.queued_at, w.created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
) RETURNING ` + workerColumns
	return scanWorker(r.db.QueryRowContext(ctx, query, maxGlobal, maxPerProject))
}

const issueDraftColumns = `id, kind, project_id, chat_id, source_message_id, bot_id, bot_name,
    group_id, group_name, sender_id, sender_name, source_text, title, body, severity, ref_id, repo, labels,
    status, issue_number, issue_url, confirmed_by_id, confirmed_by_name, created_at, updated_at, expires_at`

func scanIssueDraft(row interface{ Scan(...any) error }) (*IssueDraft, error) {
	var (
		d      IssueDraft
		labels string
	)
	err := row.Scan(&d.ID, &d.Kind, &d.ProjectID, &d.ChatID, &d.SourceMessageID, &d.BotID, &d.BotName,
		&d.GroupID, &d.GroupName, &d.SenderID, &d.SenderName, &d.SourceText, &d.Title, &d.Body,
		&d.Severity, &d.RefID, &d.Repo, &labels, &d.Status, &d.Issue, &d.IssueURL,
		&d.ConfirmedByID, &d.ConfirmedByName, &d.CreatedAt, &d.UpdatedAt, &d.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if labels != "" {
		_ = json.Unmarshal([]byte(labels), &d.Labels)
	}
	return &d, nil
}

func (r *PgRepo) CreateIssueDraft(ctx context.Context, d *IssueDraft) (bool, error) {
	now := time.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.ExpiresAt.IsZero() {
		d.ExpiresAt = now.Add(issueDraftTTL)
	}
	if d.Kind == "" {
		d.Kind = "chat"
	}
	labels, _ := json.Marshal(d.Labels)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("scheduler: issue draft tx: %w", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `
INSERT INTO scheduler_issue_drafts
 (id, kind, project_id, chat_id, source_message_id, bot_id, bot_name, group_id, group_name,
  sender_id, sender_name, source_text, title, body, severity, ref_id, repo, labels,
  status, created_at, updated_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
ON CONFLICT (source_message_id) WHERE source_message_id <> '' DO NOTHING
RETURNING `+issueDraftColumns,
		d.ID, d.Kind, d.ProjectID, d.ChatID, d.SourceMessageID, d.BotID, d.BotName, d.GroupID, d.GroupName,
		d.SenderID, d.SenderName, d.SourceText, d.Title, d.Body, d.Severity, d.RefID, d.Repo, string(labels),
		d.Status, d.CreatedAt, d.UpdatedAt, d.ExpiresAt)
	got, err := scanIssueDraft(row)
	if err != nil {
		return false, fmt.Errorf("scheduler: create issue draft: %w", err)
	}
	if got == nil {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$4, updated_at=$5
WHERE project_id=$1 AND chat_id=$2 AND status=$3 AND id<>$6`,
		d.ProjectID, d.ChatID, IssueDraftPending, IssueDraftSuperseded, now, d.ID); err != nil {
		return false, fmt.Errorf("scheduler: supersede issue drafts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("scheduler: commit issue draft: %w", err)
	}
	*d = *got
	return true, nil
}

func (r *PgRepo) LatestPendingIssueDraft(ctx context.Context, projectID, chatID string, now time.Time) (*IssueDraft, error) {
	_, _ = r.db.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$4, updated_at=$3 WHERE project_id=$1 AND chat_id=$2 AND status=$5 AND expires_at <= $3`,
		projectID, chatID, now, IssueDraftExpired, IssueDraftPending)
	got, err := scanIssueDraft(r.db.QueryRowContext(ctx, `SELECT `+issueDraftColumns+`
FROM scheduler_issue_drafts
WHERE project_id=$1 AND chat_id=$2 AND status=$3 AND expires_at>$4
ORDER BY created_at DESC LIMIT 1`, projectID, chatID, IssueDraftPending, now))
	if err != nil {
		return nil, fmt.Errorf("scheduler: latest issue draft: %w", err)
	}
	return got, nil
}

func (r *PgRepo) ClaimIssueDraft(ctx context.Context, id, projectID, chatID, actorID, actorName string, now time.Time) (*IssueDraft, error) {
	got, err := scanIssueDraft(r.db.QueryRowContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$6, confirmed_by_id=$7, confirmed_by_name=$8, updated_at=$9
WHERE id=$1 AND project_id=$2 AND chat_id=$3 AND status=$4 AND expires_at>$5
RETURNING `+issueDraftColumns,
		id, projectID, chatID, IssueDraftPending, now, IssueDraftCreating, actorID, actorName, now))
	if err != nil {
		return nil, fmt.Errorf("scheduler: claim issue draft: %w", err)
	}
	return got, nil
}

func (r *PgRepo) FinishIssueDraft(ctx context.Context, id, status string, issue int, issueURL string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$2, issue_number=$3, issue_url=$4, updated_at=$5 WHERE id=$1`, id, status, issue, issueURL, now)
	if err != nil {
		return fmt.Errorf("scheduler: finish issue draft: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrIssueDraftNotFound
	}
	return nil
}

func (r *PgRepo) ReleaseIssueDraft(ctx context.Context, id string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduler_issue_drafts
SET status=$3, updated_at=$2 WHERE id=$1 AND status=$4`, id, now, IssueDraftPending, IssueDraftCreating)
	if err != nil {
		return fmt.Errorf("scheduler: release issue draft: %w", err)
	}
	return nil
}

// MarkIssueDraftSource 把确认结果回写到来源业务表；崩溃上报需要看到真实 Issue 才能去重和补评论。
func (r *PgRepo) MarkIssueDraftSource(ctx context.Context, d *IssueDraft, status string, issue int, issueURL string) error {
	if d == nil || d.Kind != "crash" || d.RefID == "" {
		return nil
	}
	reportID, err := strconv.ParseInt(d.RefID, 10, 64)
	if err != nil {
		return fmt.Errorf("scheduler: bad crash report id %q: %w", d.RefID, err)
	}
	crashStatus := "pending"
	if status == IssueDraftCreated {
		crashStatus = "created"
	} else if status == IssueDraftCancelled {
		crashStatus = "cancelled"
	}
	_, err = r.db.ExecContext(ctx, `UPDATE crash_reports
SET issue_number=$2, issue_url=$3, status=$4, updated_at=now() WHERE id=$1`,
		reportID, issue, issueURL, crashStatus)
	return err
}
