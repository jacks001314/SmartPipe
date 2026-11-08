package store

import (
	"context"
	"fmt"
	"time"
)

// GitHubRepository 是入库的 GitHub 仓库快照，供项目创建时选择。
type GitHubRepository struct {
	FullName      string     `json:"full_name"`
	GHID          int64      `json:"id"`
	Name          string     `json:"name"`
	Owner         string     `json:"owner"`
	Private       bool       `json:"private"`
	Description   string     `json:"description"`
	DefaultBranch string     `json:"default_branch"`
	Language      string     `json:"language"`
	Stars         int        `json:"stars"`
	OpenIssues    int        `json:"open_issues"`
	UpdatedAt     string     `json:"updated_at"`
	HTMLURL       string     `json:"html_url"`
	Archived      bool       `json:"archived"`
	Fork          bool       `json:"fork"`
	Source        string     `json:"source"`
	UserVisible   bool       `json:"user_visible"`
	SyncedAt      time.Time  `json:"synced_at"`
	LocalPath     string     `json:"local_path"`
	ClonedAt      *time.Time `json:"cloned_at,omitempty"`
	CloneError    string     `json:"clone_error,omitempty"`
	CloneStatus   string     `json:"clone_status"`
	CloneProgress int        `json:"clone_progress"`
	ClonePhase    string     `json:"clone_phase"`
	CloneMessage  string     `json:"clone_message"`
	CloneStarted  *time.Time `json:"clone_started_at,omitempty"`
	CloneBytes    int64      `json:"clone_bytes"`
}

const githubRepoInsertColumns = `full_name, gh_id, name, owner, private, description, default_branch, language,
	stars, open_issues, updated_at, html_url, archived, fork, source, user_visible, synced_at`

const githubRepoColumns = githubRepoInsertColumns + `, local_path, cloned_at, clone_error, clone_status,
	clone_progress, clone_phase, clone_message, clone_started_at, clone_bytes`

// SyncGitHubRepositories 用一次拉取结果整体刷新仓库库：按 full_name upsert，
// 并删除本次没有出现的仓库（项目只保存 URL 文本，不受影响）。
func (s *Store) SyncGitHubRepositories(ctx context.Context, items []GitHubRepository) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin github repo sync: %w", err)
	}
	for _, item := range items {
		if item.FullName == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO github_repositories (`+githubRepoInsertColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (full_name) DO UPDATE SET
    gh_id = EXCLUDED.gh_id,
    name = EXCLUDED.name,
    owner = EXCLUDED.owner,
    private = EXCLUDED.private,
    description = EXCLUDED.description,
    default_branch = EXCLUDED.default_branch,
    language = EXCLUDED.language,
    stars = EXCLUDED.stars,
    open_issues = EXCLUDED.open_issues,
    updated_at = EXCLUDED.updated_at,
    html_url = EXCLUDED.html_url,
    archived = EXCLUDED.archived,
    fork = EXCLUDED.fork,
    source = EXCLUDED.source,
    user_visible = EXCLUDED.user_visible,
    synced_at = EXCLUDED.synced_at`,
			item.FullName, item.GHID, item.Name, item.Owner, item.Private, item.Description, item.DefaultBranch,
			item.Language, item.Stars, item.OpenIssues, item.UpdatedAt, item.HTMLURL, item.Archived, item.Fork,
			item.Source, item.UserVisible, now,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: upsert github repo %s: %w", item.FullName, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_repositories WHERE synced_at < $1`, now); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: prune github repos: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit github repo sync: %w", err)
	}
	return nil
}

// ListGitHubRepositories 返回入库仓库与最近一次同步时间。
func (s *Store) ListGitHubRepositories(ctx context.Context) ([]GitHubRepository, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+githubRepoColumns+`
FROM github_repositories
ORDER BY (local_path <> '') DESC, private DESC, fork ASC, lower(full_name) ASC`)
	if err != nil {
		return nil, "", fmt.Errorf("store: list github repositories: %w", err)
	}
	defer rows.Close()
	out := make([]GitHubRepository, 0, 128)
	var latest time.Time
	for rows.Next() {
		var item GitHubRepository
		if err := rows.Scan(&item.FullName, &item.GHID, &item.Name, &item.Owner, &item.Private, &item.Description,
			&item.DefaultBranch, &item.Language, &item.Stars, &item.OpenIssues, &item.UpdatedAt, &item.HTMLURL,
			&item.Archived, &item.Fork, &item.Source, &item.UserVisible, &item.SyncedAt,
			&item.LocalPath, &item.ClonedAt, &item.CloneError, &item.CloneStatus,
			&item.CloneProgress, &item.ClonePhase, &item.CloneMessage, &item.CloneStarted, &item.CloneBytes); err != nil {
			return nil, "", fmt.Errorf("store: scan github repository: %w", err)
		}
		if item.SyncedAt.After(latest) {
			latest = item.SyncedAt
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: iterate github repositories: %w", err)
	}
	syncedAt := ""
	if !latest.IsZero() {
		syncedAt = latest.Format(time.RFC3339)
	}
	return out, syncedAt, nil
}

// SetGitHubRepoCloneState 记录仓库克隆状态：cloning / cloned / failed。
func (s *Store) SetGitHubRepoCloneState(ctx context.Context, fullName, status, localPath, cloneErr string) error {
	var clonedAt any
	var startedAt any
	progress := 0
	phase := ""
	switch status {
	case "cloned":
		clonedAt = time.Now()
		progress = 100
		phase = "完成"
	case "cloning":
		startedAt = time.Now()
		phase = "准备中"
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE github_repositories
   SET local_path = CASE WHEN $4 = 'cloned' THEN $2 ELSE local_path END,
       cloned_at = CASE WHEN $3::timestamptz IS NULL THEN cloned_at ELSE $3::timestamptz END,
       clone_error = $5,
       clone_status = $4,
       clone_progress = $6,
       clone_phase = $7,
       clone_message = '',
       clone_bytes = CASE WHEN $4 = 'cloning' THEN 0 ELSE clone_bytes END,
       clone_started_at = CASE WHEN $8::timestamptz IS NULL THEN clone_started_at ELSE $8::timestamptz END
 WHERE lower(full_name) = lower($1)`, fullName, localPath, clonedAt, status, cloneErr, progress, phase, startedAt)
	if err != nil {
		return fmt.Errorf("store: update github repo clone: %w", err)
	}
	return nil
}

// SetGitHubRepoCloneProgress 记录克隆进度（阶段、总体百分比、原始进度行）。
func (s *Store) SetGitHubRepoCloneProgress(ctx context.Context, fullName, phase string, progress int, message string) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	if len(message) > 240 {
		message = message[:240]
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE github_repositories
   SET clone_progress = $2, clone_phase = $3, clone_message = $4
 WHERE lower(full_name) = lower($1)`, fullName, progress, phase, message)
	if err != nil {
		return fmt.Errorf("store: update github repo clone progress: %w", err)
	}
	return nil
}

// ResetGitHubRepoClones 在服务启动时把中断的克隆标记为失败，避免状态一直停在 cloning。
func (s *Store) ResetGitHubRepoClones(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE github_repositories
   SET clone_status = 'failed', clone_error = '服务重启，克隆中断，请重试'
 WHERE clone_status = 'cloning'`)
	if err != nil {
		return fmt.Errorf("store: reset github repo clones: %w", err)
	}
	return nil
}

// SetGitHubRepoCloneBytes 记录克隆目录已有字节数，供进度条显示“已下载”。
func (s *Store) SetGitHubRepoCloneBytes(ctx context.Context, fullName string, size int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE github_repositories SET clone_bytes = $2 WHERE lower(full_name) = lower($1)`, fullName, size)
	if err != nil {
		return fmt.Errorf("store: update github repo clone bytes: %w", err)
	}
	return nil
}
