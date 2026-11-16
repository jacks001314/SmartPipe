package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"smartpipe/backend/internal/releasev2"
)

const agentSessionColumns = `id, run_id, project_id, status, actor, model, thread_id, turn_id,
       summary, last_error, token_id, started_at, finished_at, created_at, updated_at`

// SetReleaseV2RunAgentThreadID 记住这条运行的 Codex 线程 id，供后续会话续用。
// 刻意不碰 updated_at：attention worker 用它判断「这个运行停住了多久」，
// 写线程 id 不是状态变化，提前刷新会让接管冷却失效。
func (s *Store) SetReleaseV2RunAgentThreadID(ctx context.Context, runID, threadID string) error {
	runID, threadID = strings.TrimSpace(runID), strings.TrimSpace(threadID)
	if runID == "" || threadID == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE release_v2_runs SET agent_thread_id=$2 WHERE id=$1`, runID, threadID); err != nil {
		return fmt.Errorf("store: set release v2 run agent thread: %w", err)
	}
	return nil
}

func scanAgentSession(row interface{ Scan(...any) error }) (*releasev2.AgentSession, error) {
	var s releasev2.AgentSession
	var finished sql.NullTime
	if err := row.Scan(&s.ID, &s.RunID, &s.ProjectID, &s.Status, &s.Actor, &s.Model, &s.ThreadID, &s.TurnID,
		&s.Summary, &s.LastError, &s.TokenID, &s.StartedAt, &finished, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if finished.Valid {
		t := finished.Time
		s.FinishedAt = &t
	}
	return &s, nil
}

// CreateAgentSession registers a release agent session before the Codex
// process starts so the console can show it even if the process never runs.
func (s *Store) CreateAgentSession(ctx context.Context, in *releasev2.AgentSession) error {
	if in.Status == "" {
		in.Status = releasev2.AgentSessionStarting
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_agent_sessions (id, run_id, project_id, status, actor, model, thread_id, turn_id, summary, last_error, token_id, started_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
RETURNING started_at, created_at, updated_at`,
		in.ID, in.RunID, in.ProjectID, in.Status, in.Actor, in.Model, in.ThreadID, in.TurnID, in.Summary, in.LastError, in.TokenID)
	if err := row.Scan(&in.StartedAt, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return fmt.Errorf("store: create release agent session: %w", err)
	}
	return nil
}

// UpdateAgentSession writes the mutable fields of a session.
func (s *Store) UpdateAgentSession(ctx context.Context, in *releasev2.AgentSession) error {
	var finished any
	if in.FinishedAt != nil {
		finished = *in.FinishedAt
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE release_v2_agent_sessions SET
  status=$2, model=$3, thread_id=$4, turn_id=$5, summary=$6, last_error=$7, token_id=$8,
  finished_at=$9, updated_at=now()
WHERE id=$1`,
		in.ID, in.Status, in.Model, in.ThreadID, in.TurnID, in.Summary, in.LastError, in.TokenID, finished)
	if err != nil {
		return fmt.Errorf("store: update release agent session: %w", err)
	}
	return nil
}

func (s *Store) GetAgentSession(ctx context.Context, id string) (*releasev2.AgentSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentSessionColumns+` FROM release_v2_agent_sessions WHERE id=$1`, id)
	out, err := scanAgentSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get release agent session: %w", err)
	}
	return out, nil
}

// LatestAgentSession returns the newest session for a run, if any.
func (s *Store) LatestAgentSession(ctx context.Context, runID string) (*releasev2.AgentSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentSessionColumns+`
  FROM release_v2_agent_sessions WHERE run_id=$1 ORDER BY created_at DESC LIMIT 1`, runID)
	out, err := scanAgentSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: latest release agent session: %w", err)
	}
	return out, nil
}

// ListAgentSessions returns recent sessions of a run, newest first.
func (s *Store) ListAgentSessions(ctx context.Context, runID string, limit int) ([]releasev2.AgentSession, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentSessionColumns+`
  FROM release_v2_agent_sessions WHERE run_id=$1 ORDER BY created_at DESC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list release agent sessions: %w", err)
	}
	defer rows.Close()
	out := []releasev2.AgentSession{}
	for rows.Next() {
		item, err := scanAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// CloseStaleAgentSessions marks sessions left behind by a restart as failed so
// the console does not show a phantom running agent forever, and hands their
// runs back to the attention worker.
//
// 只关会话是不够的：控制面在制品就绪之后、灌库/发布之前重启，run 会停在
// artifacts_ready，既没有活跃会话也不在 attention 的候选状态里，于是一直
// 停着。这里在关掉僵尸会话后，把「没有活跃会话、也没有目标在构建机跑」的
// Agent 阶段运行交回 needs_attention。
func (s *Store) CloseStaleAgentSessions(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH closed AS (
  UPDATE release_v2_agent_sessions
     SET status='failed', last_error=COALESCE(NULLIF(last_error,''),'agent process lost'),
         finished_at=now(), updated_at=now()
   WHERE status IN ('starting','running')
  RETURNING run_id
)
SELECT run_id FROM closed`)
	if err != nil {
		return 0, fmt.Errorf("store: close stale release agent sessions: %w", err)
	}
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return 0, err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(runIDs) == 0 {
		return 0, nil
	}
	// 只交回「进程被硬杀」的那几个运行：会话已经自己结束的运行（比如被
	// 版本冲突拦下来、Agent 有意停在 artifacts_ready）不在此列，否则每次
	// 重启都会把它们重新拉起来撞同一个墙。
	if _, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs r SET status='needs_attention', updated_at=now()
 WHERE r.id = ANY($1::text[])
   AND r.status IN ('artifacts_ready','recovering','staged','publishing')
   AND NOT EXISTS (
     SELECT 1 FROM release_v2_targets t
      WHERE t.run_id=r.id AND t.status IN ('queued','leased','building','verifying','uploaded')
   )`, runIDs); err != nil {
		return len(runIDs), fmt.Errorf("store: hand back orphaned release runs: %w", err)
	}
	return len(runIDs), nil
}

// MarkReleaseV2RunNeedsAttention 把仍然进行中的运行交回 Agent。Agent 会话
// 异常退出或超时后，运行本身不会自动进入 needs_attention，attention worker
// 也就不会再接管；这里补上这一步，保证失败能自愈而不是停住。
//
// 有目标还在构建机上跑时不动：Agent 会话的整轮超时（默认 30 分钟）比
// Electron 全量构建短，会话结束时把 run 拽回 needs_attention 只会白烧一次
// 重试预算。留在 building，等目标完成后由 runner 推进到制品就绪/已构建。
func (s *Store) MarkReleaseV2RunNeedsAttention(ctx context.Context, runID, reason string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET status='needs_attention', updated_at=now()
 WHERE id=$1 AND status IN ('queued','preparing','building','verifying','artifacts_ready','recovering','staged','publishing')
   AND NOT EXISTS (
     SELECT 1 FROM release_v2_targets t
      WHERE t.run_id=$1 AND t.status IN ('queued','leased','building','verifying','uploaded')
   )`, runID)
	if err != nil {
		return false, fmt.Errorf("store: mark release v2 run needs attention: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 && strings.TrimSpace(reason) != "" {
		_ = appendReleaseV2EventTx(ctx, s.db, releasev2.Event{
			RunID: runID, Actor: "release-agent", Kind: "run.status",
			Summary: "发布 Agent 会话结束，运行交回自动接管",
			Detail:  map[string]any{"reason": reason},
		})
	}
	return n == 1, nil
}

// RequeueOrphanedReleaseV2Runs 把控制面重启后失去 Agent 的进行中运行交回
// needs_attention，让 attention worker 重新接管。进程内的 Agent 会话随重启
// 消失，但数据库状态不会自己回到 needs_attention；不处理的话这些运行会
// 永远停在 building/recovering，只能人工介入。
func (s *Store) RequeueOrphanedReleaseV2Runs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH moved AS (
  UPDATE release_v2_runs SET status='needs_attention', updated_at=now()
   WHERE status IN ('queued','preparing','building','verifying','artifacts_ready','recovering','staged','publishing')
     AND NOT EXISTS (
       SELECT 1 FROM release_v2_targets t
        WHERE t.run_id=release_v2_runs.id
          AND t.status IN ('queued','leased','building','verifying','uploaded')
     )
  RETURNING id
)
INSERT INTO release_v2_events (run_id, actor, kind, summary, detail)
SELECT id, 'system:startup', 'run.status', '控制面重启后交回 Agent 接管', '{"reason":"control plane restarted"}'::jsonb FROM moved
RETURNING run_id`)
	if err != nil {
		return nil, fmt.Errorf("store: requeue orphaned release v2 runs: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ParkReleaseV2Run 把运行显式停在 needs_attention，并写入终止性错误码。
// 与 MarkReleaseV2RunNeedsAttention 不同，重复调用是幂等的：状态和错误码
// 都没变时不再写事件。带错误码的运行也不会被 release-service 按"目标都
// ready"的事实收口回 artifacts_ready，从而避免和 attention worker 的停靠
// 形成每轮一次的无限往返（生产事故：事件面板每 40 秒刷一次）。
func (s *Store) ParkReleaseV2Run(ctx context.Context, runID, code, reason string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET status='needs_attention', error_code=$2, error_message=$3, updated_at=now()
 WHERE id=$1 AND (status<>'needs_attention' OR error_code<>$2)`, runID, code, reason)
	if err != nil {
		return false, fmt.Errorf("store: park release v2 run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		_ = appendReleaseV2EventTx(ctx, s.db, releasev2.Event{
			RunID: runID, Actor: "system:attention", Kind: "run.needs_attention",
			Summary: reason,
			Detail:  map[string]any{"code": code},
		})
	}
	return n == 1, nil
}
