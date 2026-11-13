package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ExecutorHost 记录一台构建执行器的部署位置：它部署在哪个 SSH 资源上、
// 装到哪个目录。删除执行器时靠它回到目标机停止服务并卸载文件。
type ExecutorHost struct {
	ExecutorID string    `json:"executor_id"`
	ResourceID string    `json:"resource_id"`
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	Username   string    `json:"username"`
	InstallDir string    `json:"install_dir"`
	DeployedAt time.Time `json:"deployed_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UpsertExecutorHost 记录或更新一次部署位置；同一执行器重新部署会覆盖。
func (s *Store) UpsertExecutorHost(ctx context.Context, h ExecutorHost) error {
	if h.Port == 0 {
		h.Port = 22
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO release_v2_executor_hosts (executor_id, resource_id, host, port, username, install_dir)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (executor_id) DO UPDATE SET
  resource_id=EXCLUDED.resource_id, host=EXCLUDED.host, port=EXCLUDED.port,
  username=EXCLUDED.username, install_dir=EXCLUDED.install_dir, updated_at=now()`,
		h.ExecutorID, h.ResourceID, h.Host, h.Port, h.Username, h.InstallDir)
	if err != nil {
		return fmt.Errorf("store: upsert executor host: %w", err)
	}
	return nil
}

// GetExecutorHost 返回部署位置；没有部署记录时返回 ErrNotFound。
func (s *Store) GetExecutorHost(ctx context.Context, executorID string) (*ExecutorHost, error) {
	var h ExecutorHost
	err := s.db.QueryRowContext(ctx, `
SELECT executor_id, resource_id, host, port, username, install_dir, deployed_at, updated_at
  FROM release_v2_executor_hosts WHERE executor_id=$1`, executorID).
		Scan(&h.ExecutorID, &h.ResourceID, &h.Host, &h.Port, &h.Username, &h.InstallDir, &h.DeployedAt, &h.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get executor host: %w", err)
	}
	return &h, nil
}

// DeleteExecutorHost 删除部署位置记录。
func (s *Store) DeleteExecutorHost(ctx context.Context, executorID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM release_v2_executor_hosts WHERE executor_id=$1`, executorID); err != nil {
		return fmt.Errorf("store: delete executor host: %w", err)
	}
	return nil
}

// DeleteExecutor 删除执行器注册记录。
func (s *Store) DeleteExecutor(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM release_v2_executors WHERE id=$1`, id); err != nil {
		return fmt.Errorf("store: delete release executor: %w", err)
	}
	return nil
}

// CountActiveTargetsForExecutor 统计仍占用该执行器的 target，删除前用来避免拆掉正在跑的构建。
func (s *Store) CountActiveTargetsForExecutor(ctx context.Context, id string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM release_v2_targets
 WHERE executor_id=$1 AND status IN ('leased','building','verifying')`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count active targets: %w", err)
	}
	return n, nil
}
