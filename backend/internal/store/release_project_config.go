package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"smartpipe/backend/internal/releasev2"
)

// GetProjectReleaseConfig 读取项目级发版配置。没有配置时返回默认值，
// 让调用方可以在项目页先填写再保存。
func (s *Store) GetProjectReleaseConfig(ctx context.Context, projectID string) (*releasev2.ProjectReleaseConfig, error) {
	var (
		cfg        releasev2.ProjectReleaseConfig
		builds     []byte
		channels   []byte
		servers    []byte
		publishing []byte
		policies   []byte
	)
	row := s.db.QueryRowContext(ctx, `
SELECT project_id, builds, manifest_path, version_file, version_json_path,
       build_version_template, notes_path, channel_order, update_server_ids,
       publishing, policies, updated_at
  FROM release_project_configs WHERE project_id=$1`, projectID)
	err := row.Scan(&cfg.ProjectID, &builds, &cfg.ManifestPath, &cfg.VersionFile, &cfg.VersionJSONPath,
		&cfg.BuildVersionTemplate, &cfg.NotesPath, &channels, &servers, &publishing, &policies, &cfg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		cfg = releasev2.ProjectReleaseConfig{ProjectID: projectID}
		if nerr := cfg.Normalize(); nerr != nil {
			return nil, nerr
		}
		return &cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get project release config: %w", err)
	}
	if len(builds) > 0 {
		_ = json.Unmarshal(builds, &cfg.Builds)
	}
	if len(channels) > 0 {
		_ = json.Unmarshal(channels, &cfg.ChannelOrder)
	}
	if len(servers) > 0 {
		_ = json.Unmarshal(servers, &cfg.UpdateServerIDs)
	}
	if len(publishing) > 0 {
		_ = json.Unmarshal(publishing, &cfg.Publishing)
	}
	if len(policies) > 0 {
		_ = json.Unmarshal(policies, &cfg.Policies)
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// UpsertProjectReleaseConfig 保存项目级发版配置，重复保存幂等。
func (s *Store) UpsertProjectReleaseConfig(ctx context.Context, cfg *releasev2.ProjectReleaseConfig) error {
	if cfg == nil {
		return fmt.Errorf("%w: project release config is nil", releasev2.ErrInvalid)
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO release_project_configs (
  project_id, builds, manifest_path, version_file, version_json_path,
  build_version_template, notes_path, channel_order, update_server_ids,
  publishing, policies, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
ON CONFLICT (project_id) DO UPDATE SET
  builds=EXCLUDED.builds, manifest_path=EXCLUDED.manifest_path,
  version_file=EXCLUDED.version_file, version_json_path=EXCLUDED.version_json_path,
  build_version_template=EXCLUDED.build_version_template, notes_path=EXCLUDED.notes_path,
  channel_order=EXCLUDED.channel_order, update_server_ids=EXCLUDED.update_server_ids,
  publishing=EXCLUDED.publishing, policies=EXCLUDED.policies,
  updated_at=now()`,
		cfg.ProjectID, mustJSON(cfg.Builds), cfg.ManifestPath, cfg.VersionFile, cfg.VersionJSONPath,
		cfg.BuildVersionTemplate, cfg.NotesPath, mustJSON(cfg.ChannelOrder), mustJSON(cfg.UpdateServerIDs),
		mustJSON(cfg.Publishing), mustJSON(cfg.Policies))
	if err != nil {
		return fmt.Errorf("store: upsert project release config: %w", err)
	}
	cfg.UpdatedAt = time.Now().UTC()
	return nil
}

// UpdateRunIntake 记录这次发布选中的升级服务和构建机，供 Agent 与审计读取。
func (s *Store) UpdateRunIntake(ctx context.Context, runID string, executorIDs, updateServers []string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET executor_ids=$2, update_servers=$3, updated_at=now() WHERE id=$1`,
		runID, mustJSON(executorIDs), mustJSON(updateServers))
	if err != nil {
		return fmt.Errorf("store: update release intake: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
