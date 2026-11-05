// Package store 是 SmartPipe 的持久层：项目注册表、凭据密文、审计日志
// 与运行时资源索引。业务事实（Issue、评论、PR）仍以 GitHub 为准，
// 这里只保存桥接和配置所需的最少数据。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"smartpipe/backend/internal/secretbox"
)

// ErrNotFound 表示目标记录不存在。
var ErrNotFound = errors.New("store: not found")

// ErrConflict 表示唯一约束或并发写入冲突。
var ErrConflict = errors.New("store: conflict")

// Store 是所有仓储方法的入口。
type Store struct {
	db  *sql.DB
	box *secretbox.Box
}

// Open 连接数据库并完成一次 ping。迁移需显式调用 Migrate。
func Open(ctx context.Context, dsn string, box *secretbox.Box) (*Store, error) {
	if box == nil {
		return nil, errors.New("store: secretbox is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{db: db, box: box}, nil
}

// Close 释放连接池。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接，供测试与运维脚本使用。
func (s *Store) DB() *sql.DB { return s.db }

// Ping 检查数据库可用性，运维接口用。
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
