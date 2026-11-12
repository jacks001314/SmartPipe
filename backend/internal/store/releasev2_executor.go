package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"smartpipe/backend/internal/releasev2"
)

// GetExecutor returns one registered build executor.
func (s *Store) GetExecutor(ctx context.Context, id string) (*releasev2.Executor, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, os, arch, labels, capabilities, version, status, last_heartbeat_at, last_error, created_at, updated_at
  FROM release_v2_executors WHERE id=$1`, id)
	var x releasev2.Executor
	var labels, caps []byte
	var heartbeat sql.NullTime
	if err := row.Scan(&x.ID, &x.OS, &x.Arch, &labels, &caps, &x.Version, &x.Status, &heartbeat,
		&x.LastError, &x.CreatedAt, &x.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get release v2 executor: %w", err)
	}
	_ = json.Unmarshal(labels, &x.Labels)
	_ = json.Unmarshal(caps, &x.Capabilities)
	x.LastHeartbeatAt = nullTimePtr(heartbeat)
	return &x, nil
}
