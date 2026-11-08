package store

import "time"

// Project 是一个接入 SmartPipe 的目标项目。
type Project struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	GitHubProvider       string    `json:"github_provider"`
	GitHubRepoURL        string    `json:"github_repo_url"`
	GitHubDefaultBranch  string    `json:"github_default_branch"`
	GitHubIssueRepo      string    `json:"github_issue_repo"`
	LabelsPrefix         string    `json:"labels_prefix"`
	CodeownersRequired   bool      `json:"codeowners_required"`
	ApproverTeams        string    `json:"approver_teams"`
	VerifyProfilePath    string    `json:"verify_profile_path"`
	ReleaseProfile       string    `json:"release_profile"`
	BluexGroupIDs        string    `json:"bluex_group_ids"`
	LanxinBotID          string    `json:"lanxin_bot_id"`
	LeadAgentTemplate    string    `json:"lead_agent_template"`
	CodexModelProvider   string    `json:"codex_model_provider"`
	CodexModel           string    `json:"codex_model"`
	CodexReasoningEffort string    `json:"codex_reasoning_effort"`
	ToolProfile          string    `json:"tool_profile"`
	LocalRepoPath        string    `json:"local_repo_path"`
	Enabled              bool      `json:"enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Credential 是凭据的对外视图：只有掩码，没有明文。
type Credential struct {
	ID              string         `json:"id"`
	ProjectID       *string        `json:"project_id"`
	Kind            string         `json:"kind"`
	Name            string         `json:"name"`
	Mask            string         `json:"mask"`
	Metadata        map[string]any `json:"metadata"`
	Enabled         bool           `json:"enabled"`
	LastTestedAt    *time.Time     `json:"last_tested_at"`
	LastTestOK      *bool          `json:"last_test_ok"`
	LastTestMessage string         `json:"last_test_message"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// AuditEntry 是一次管理操作或工具调用的审计记录。
type AuditEntry struct {
	ID         int64          `json:"id"`
	At         time.Time      `json:"at"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	ProjectID  *string        `json:"project_id"`
	Result     string         `json:"result"`
	Detail     map[string]any `json:"detail"`
	TraceID    string         `json:"trace_id"`
}
