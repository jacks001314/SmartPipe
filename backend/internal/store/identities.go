package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UserIdentity 把渠道用户（如蓝信）映射到 GitHub 账号。
// project_id 为空表示全局映射；项目级映射优先。
type UserIdentity struct {
	ID             int64     `json:"id"`
	Platform       string    `json:"platform"`
	PlatformUserID string    `json:"platform_user_id"`
	ProjectID      *string   `json:"project_id"`
	GitHubLogin    string    `json:"github_login"`
	DisplayName    string    `json:"display_name"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const identityColumns = `id, platform, platform_user_id, NULLIF(project_id,'') AS project_id, github_login, display_name, enabled, created_at, updated_at`

// UpsertUserIdentity 新增或更新一条映射（同平台 + 同用户 + 同项目唯一）。
func (s *Store) UpsertUserIdentity(ctx context.Context, in UserIdentity) (*UserIdentity, error) {
	in.Platform = strings.ToLower(strings.TrimSpace(in.Platform))
	in.PlatformUserID = strings.TrimSpace(in.PlatformUserID)
	in.GitHubLogin = strings.TrimPrefix(strings.TrimSpace(in.GitHubLogin), "@")
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.Platform == "" || in.PlatformUserID == "" {
		return nil, fmt.Errorf("store: platform 与 platform_user_id 必填")
	}
	if in.GitHubLogin == "" {
		return nil, fmt.Errorf("store: github_login 必填")
	}
	project := ""
	if in.ProjectID != nil {
		project = strings.TrimSpace(*in.ProjectID)
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO user_identities (platform, platform_user_id, project_id, github_login, display_name, enabled)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (platform, platform_user_id, project_id) DO UPDATE
SET github_login = EXCLUDED.github_login, display_name = EXCLUDED.display_name,
    enabled = EXCLUDED.enabled, updated_at = now()
RETURNING `+identityColumns,
		in.Platform, in.PlatformUserID, project, in.GitHubLogin, in.DisplayName, in.Enabled)
	return scanIdentity(row)
}

// ListUserIdentities 列出映射；platform 为空表示全部。
func (s *Store) ListUserIdentities(ctx context.Context, platform string, projectID *string) ([]UserIdentity, error) {
	project := ""
	if projectID != nil {
		project = strings.TrimSpace(*projectID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+identityColumns+` FROM user_identities
WHERE ($1 = '' OR platform = $1) AND ($2 = '' OR project_id = $2)
ORDER BY id DESC LIMIT 500`, strings.ToLower(strings.TrimSpace(platform)), project)
	if err != nil {
		return nil, fmt.Errorf("store: list identities: %w", err)
	}
	defer rows.Close()
	out := make([]UserIdentity, 0, 16)
	for rows.Next() {
		item, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// FindUserIdentity 查询某个渠道用户在项目下的映射（项目级优先，其次全局）。
func (s *Store) FindUserIdentity(ctx context.Context, platform, platformUserID string, projectID *string) (*UserIdentity, error) {
	if strings.TrimSpace(platformUserID) == "" {
		return nil, ErrNotFound
	}
	project := ""
	if projectID != nil {
		project = strings.TrimSpace(*projectID)
	}
	row := s.db.QueryRowContext(ctx, `
SELECT `+identityColumns+` FROM user_identities
WHERE platform = $1 AND platform_user_id = $2 AND enabled AND project_id IN ($3, '')
ORDER BY (project_id = $3) DESC, id DESC LIMIT 1`, strings.ToLower(strings.TrimSpace(platform)), strings.TrimSpace(platformUserID), project)
	return scanIdentity(row)
}

// DeleteUserIdentity 删除一条映射。
func (s *Store) DeleteUserIdentity(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_identities WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete identity: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanIdentity(row interface{ Scan(dest ...any) error }) (*UserIdentity, error) {
	var (
		item    UserIdentity
		project *string
	)
	if err := row.Scan(&item.ID, &item.Platform, &item.PlatformUserID, &project, &item.GitHubLogin,
		&item.DisplayName, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: scan identity: %w", err)
	}
	item.ProjectID = project
	return &item, nil
}
