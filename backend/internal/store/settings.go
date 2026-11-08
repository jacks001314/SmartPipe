package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting 读取设置；不存在返回空串。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM runtime_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get setting: %w", err)
	}
	return value, nil
}

// SetSetting 写入设置。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runtime_settings (key, value, updated_at) VALUES ($1,$2,now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	if err != nil {
		return fmt.Errorf("store: set setting: %w", err)
	}
	return nil
}
