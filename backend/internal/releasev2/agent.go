package releasev2

import (
	"fmt"
	"strings"
	"time"
)

// AgentSessionStatus 是发布 Agent 会话的生命周期状态。
type AgentSessionStatus string

const (
	AgentSessionStarting  AgentSessionStatus = "starting"
	AgentSessionRunning   AgentSessionStatus = "running"
	AgentSessionFinished  AgentSessionStatus = "finished"
	AgentSessionFailed    AgentSessionStatus = "failed"
	AgentSessionCancelled AgentSessionStatus = "cancelled"
)

// Active 表示会话仍可能继续调用 MCP 工具。
func (s AgentSessionStatus) Active() bool {
	return s == AgentSessionStarting || s == AgentSessionRunning
}

// AgentSession 是一次发布 Agent 运行记录：哪个 Codex 会话、
// 用哪个模型、给哪个 run 干活、最后停在哪。
type AgentSession struct {
	ID         string             `json:"id"`
	RunID      string             `json:"run_id"`
	ProjectID  string             `json:"project_id"`
	Status     AgentSessionStatus `json:"status"`
	Actor      string             `json:"actor"`
	Model      string             `json:"model"`
	ThreadID   string             `json:"thread_id,omitempty"`
	TurnID     string             `json:"turn_id,omitempty"`
	Summary    string             `json:"summary,omitempty"`
	LastError  string             `json:"last_error,omitempty"`
	TokenID    string             `json:"token_id,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

// ReleaseAgentScopes 是发布 Agent 默认拿到的权限集合：覆盖构建、制品、
// 升级包与发布，但不包含任何长期凭据。
var ReleaseAgentScopes = []string{
	"release.read", "release.write", "release.publish",
	"executor.read", "build.read", "build.write",
	"artifact.read", "artifact.write",
	"update.read", "update.write", "update.publish",
	"github.read", "github.write",
}

// ValidateAgentScopes 拒绝未知 scope，避免前端传错字符串后静默降权。
func ValidateAgentScopes(scopes []string) ([]string, error) {
	allowed := map[string]bool{}
	for _, scope := range ReleaseAgentScopes {
		allowed[scope] = true
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !allowed[scope] {
			return nil, fmt.Errorf("%w: unknown scope %s", ErrInvalid, scope)
		}
		out = append(out, scope)
	}
	if len(out) == 0 {
		out = append(out, ReleaseAgentScopes...)
	}
	return out, nil
}

// NewAgentSessionID 生成会话 ID。
func NewAgentSessionID() string { return NewID("ragent") }
