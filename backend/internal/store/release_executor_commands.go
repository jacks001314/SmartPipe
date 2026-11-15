package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"smartpipe/backend/internal/releasev2"
)

const executorCommandColumns = `id, executor_id, run_id, target_id, kind, command, cwd, payload, status,
       exit_code, output, error_message, created_by, expires_at, started_at, finished_at, created_at, updated_at`

const maxExecutorCommandOutput = 256 << 10

// EnqueueExecutorCommand 把 Agent 的遥控动作放进构建机命令队列。
func (s *Store) EnqueueExecutorCommand(ctx context.Context, c releasev2.ExecutorCommand) (*releasev2.ExecutorCommand, error) {
	if err := c.Normalize(); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_executor_commands (executor_id, run_id, target_id, kind, command, cwd, payload, status, created_by, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
RETURNING id, status, created_at, updated_at`,
		c.ExecutorID, c.RunID, c.TargetID, string(c.Kind), c.Command, c.Cwd, mustJSON(c.Payload),
		string(c.Status), c.CreatedBy, c.ExpiresAt)
	if err := row.Scan(&c.ID, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store: enqueue executor command: %w", err)
	}
	return &c, nil
}

// ClaimExecutorCommand 原子领取该构建机最早的一条命令；没有待执行命令时返回 ErrNotFound。
func (s *Store) ClaimExecutorCommand(ctx context.Context, executorID string) (*releasev2.ExecutorCommand, error) {
	row := s.db.QueryRowContext(ctx, `
UPDATE release_executor_commands SET status='running', started_at=now(), updated_at=now()
WHERE id = (
  SELECT id FROM release_executor_commands
   WHERE executor_id=$1 AND status='queued' AND (expires_at IS NULL OR expires_at > now())
   ORDER BY id
   LIMIT 1
   FOR UPDATE SKIP LOCKED
)
RETURNING `+executorCommandColumns, executorID)
	cmd, err := scanExecutorCommand(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim executor command: %w", err)
	}
	return cmd, nil
}

// CompleteExecutorCommand 记录构建机回报的执行结果。
func (s *Store) CompleteExecutorCommand(ctx context.Context, id int64, executorID string, exitCode int, output, errorMessage string) (bool, error) {
	status := releasev2.ExecutorCommandDone
	if errorMessage != "" || exitCode != 0 {
		status = releasev2.ExecutorCommandFailed
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE release_executor_commands
   SET status=$3, exit_code=$4, output=$5, error_message=$6, finished_at=now(), updated_at=now()
 WHERE id=$1 AND executor_id=$2 AND status='running'`,
		id, executorID, string(status), exitCode, truncateCommandOutput(output), truncateCommandOutput(errorMessage))
	if err != nil {
		return false, fmt.Errorf("store: complete executor command: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetExecutorCommand 读取单条命令，供 MCP 轮询结果。
func (s *Store) GetExecutorCommand(ctx context.Context, id int64) (*releasev2.ExecutorCommand, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+executorCommandColumns+` FROM release_executor_commands WHERE id=$1`, id)
	cmd, err := scanExecutorCommand(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get executor command: %w", err)
	}
	return cmd, nil
}

// ExpireExecutorCommands 把已过期但还没被领取的命令标记为 expired。
func (s *Store) ExpireExecutorCommands(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_executor_commands
   SET status='expired', error_message='command expired before executor claimed it', finished_at=now(), updated_at=now()
 WHERE status='queued' AND expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: expire executor commands: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

type executorCommandScanner interface {
	Scan(dest ...any) error
}

func scanExecutorCommand(row executorCommandScanner) (*releasev2.ExecutorCommand, error) {
	var (
		cmd                          releasev2.ExecutorCommand
		payload                      []byte
		exitCode                     sql.NullInt64
		expiresAt, started, finished sql.NullTime
	)
	if err := row.Scan(&cmd.ID, &cmd.ExecutorID, &cmd.RunID, &cmd.TargetID, &cmd.Kind, &cmd.Command, &cmd.Cwd,
		&payload, &cmd.Status, &exitCode, &cmd.Output, &cmd.ErrorMessage, &cmd.CreatedBy,
		&expiresAt, &started, &finished, &cmd.CreatedAt, &cmd.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(payload, &cmd.Payload)
	if exitCode.Valid {
		value := int(exitCode.Int64)
		cmd.ExitCode = &value
	}
	cmd.ExpiresAt = nullTimePtr(expiresAt)
	cmd.StartedAt = nullTimePtr(started)
	cmd.FinishedAt = nullTimePtr(finished)
	return &cmd, nil
}

// 构建机输出可能很大，只保留尾部，避免把整份日志塞进数据库和 Agent 上下文。
func truncateCommandOutput(value string) string {
	value = sanitizeText(value)
	if len(value) <= maxExecutorCommandOutput {
		return value
	}
	cut := len(value) - maxExecutorCommandOutput
	for cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut++
	}
	return "...[truncated, showing tail]\n" + value[cut:]
}
