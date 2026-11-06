package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const projectColumns = `id, name, description, github_provider, github_repo_url,
    github_default_branch, github_issue_repo, labels_prefix, codeowners_required,
    approver_teams, verify_profile_path, release_profile, bluex_group_ids,
    lead_agent_template, tool_profile, codex_model_provider, codex_model,
    codex_reasoning_effort, enabled, created_at, updated_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.GitHubProvider, &p.GitHubRepoURL,
		&p.GitHubDefaultBranch, &p.GitHubIssueRepo, &p.LabelsPrefix, &p.CodeownersRequired,
		&p.ApproverTeams, &p.VerifyProfilePath, &p.ReleaseProfile, &p.BluexGroupIDs,
		&p.LeadAgentTemplate, &p.ToolProfile, &p.CodexModelProvider, &p.CodexModel,
		&p.CodexReasoningEffort, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func normalizeProject(p *Project) {
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if p.GitHubProvider == "" {
		p.GitHubProvider = "github"
	}
	if p.GitHubDefaultBranch == "" {
		p.GitHubDefaultBranch = "main"
	}
	if p.LabelsPrefix == "" {
		p.LabelsPrefix = "smartpipe"
	}
	if p.VerifyProfilePath == "" {
		p.VerifyProfilePath = "smartpipe.yaml"
	}
	if p.LeadAgentTemplate == "" {
		p.LeadAgentTemplate = "pipeline-lead-v3"
	}
	if p.ToolProfile == "" {
		p.ToolProfile = "lead"
	}
}

// CreateProject 新增项目。名称为空或重名返回错误。
func (s *Store) CreateProject(ctx context.Context, p *Project) (*Project, error) {
	if p == nil {
		return nil, errors.New("store: nil project")
	}
	normalizeProject(p)
	if p.Name == "" {
		return nil, errors.New("store: project name is required")
	}
	if p.ID == "" {
		p.ID = newID("prj")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO projects (id, name, description, github_provider, github_repo_url,
    github_default_branch, github_issue_repo, labels_prefix, codeowners_required,
    approver_teams, verify_profile_path, release_profile, bluex_group_ids,
    lead_agent_template, tool_profile, enabled, codex_model_provider,
    codex_model, codex_reasoning_effort)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
RETURNING `+projectColumns,
		p.ID, p.Name, p.Description, p.GitHubProvider, p.GitHubRepoURL,
		p.GitHubDefaultBranch, p.GitHubIssueRepo, p.LabelsPrefix, p.CodeownersRequired,
		p.ApproverTeams, p.VerifyProfilePath, p.ReleaseProfile, p.BluexGroupIDs,
		p.LeadAgentTemplate, p.ToolProfile, p.Enabled, p.CodexModelProvider,
		p.CodexModel, p.CodexReasoningEffort)
	out, err := scanProject(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: project name %q already exists", ErrConflict, p.Name)
		}
		return nil, fmt.Errorf("store: create project: %w", err)
	}
	return out, nil
}

// GetProject 按 ID 读取项目。
func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id)
	p, err := scanProject(row)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListProjects 返回全部项目，按创建时间倒序。
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	out := make([]Project, 0, 8)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpdateProject 覆盖项目的可编辑字段。
func (s *Store) UpdateProject(ctx context.Context, p *Project) (*Project, error) {
	if p == nil || p.ID == "" {
		return nil, errors.New("store: project id is required")
	}
	normalizeProject(p)
	if p.Name == "" {
		return nil, errors.New("store: project name is required")
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE projects SET
    name = $2, description = $3, github_provider = $4, github_repo_url = $5,
    github_default_branch = $6, github_issue_repo = $7, labels_prefix = $8,
    codeowners_required = $9, approver_teams = $10, verify_profile_path = $11,
    release_profile = $12, bluex_group_ids = $13, lead_agent_template = $14,
    tool_profile = $15, enabled = $16, codex_model_provider = $17,
    codex_model = $18, codex_reasoning_effort = $19, updated_at = now()
WHERE id = $1
RETURNING `+projectColumns,
		p.ID, p.Name, p.Description, p.GitHubProvider, p.GitHubRepoURL,
		p.GitHubDefaultBranch, p.GitHubIssueRepo, p.LabelsPrefix, p.CodeownersRequired,
		p.ApproverTeams, p.VerifyProfilePath, p.ReleaseProfile, p.BluexGroupIDs,
		p.LeadAgentTemplate, p.ToolProfile, p.Enabled, p.CodexModelProvider,
		p.CodexModel, p.CodexReasoningEffort)
	out, err := scanProject(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: project name %q already exists", ErrConflict, p.Name)
		}
		return nil, fmt.Errorf("store: update project: %w", err)
	}
	return out, nil
}

// SetProjectEnabled 启用或停用项目，停用后新任务应被拒绝。
func (s *Store) SetProjectEnabled(ctx context.Context, id string, enabled bool) (*Project, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE projects SET enabled = $2, updated_at = now() WHERE id = $1 RETURNING `+projectColumns, id, enabled)
	p, err := scanProject(row)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key value") || strings.Contains(msg, "SQLSTATE 23505")
}

// FindProjectByGroup 按蓝信群/会话 ID 反查项目：bluex_group_ids 是逗号分隔的列表。
// 只在配置里精确匹配；找不到时返回 ErrNotFound，调用方决定是否回落到默认项目。
func (s *Store) FindProjectByGroup(ctx context.Context, groupID string) (*Project, error) {
	target := strings.TrimSpace(groupID)
	if target == "" {
		return nil, ErrNotFound
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		for _, raw := range strings.Split(projects[i].BluexGroupIDs, ",") {
			if strings.EqualFold(strings.TrimSpace(raw), target) {
				return &projects[i], nil
			}
		}
	}
	return nil, ErrNotFound
}
