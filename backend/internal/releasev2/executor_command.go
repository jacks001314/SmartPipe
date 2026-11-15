package releasev2

import (
	"fmt"
	"strings"
	"time"
)

// ExecutorCommandKind 是 Agent 对构建机发起的遥控动作类型。
type ExecutorCommandKind string

const (
	ExecutorCommandProbe ExecutorCommandKind = "probe"
	ExecutorCommandExec  ExecutorCommandKind = "exec"
	ExecutorCommandLogs  ExecutorCommandKind = "logs"
	ExecutorCommandFiles ExecutorCommandKind = "files"
)

type ExecutorCommandStatus string

const (
	ExecutorCommandQueued    ExecutorCommandStatus = "queued"
	ExecutorCommandRunning   ExecutorCommandStatus = "running"
	ExecutorCommandDone      ExecutorCommandStatus = "done"
	ExecutorCommandFailed    ExecutorCommandStatus = "failed"
	ExecutorCommandExpired   ExecutorCommandStatus = "expired"
	ExecutorCommandCancelled ExecutorCommandStatus = "cancelled"
)

// ExecutorCommand 是 Agent 发给某台构建机的一条受审计命令。命令队列让
// Agent 能处理构建机环境问题，而不需要人工登录机器。
type ExecutorCommand struct {
	ID           int64                 `json:"id"`
	ExecutorID   string                `json:"executor_id"`
	RunID        string                `json:"run_id,omitempty"`
	TargetID     string                `json:"target_id,omitempty"`
	Kind         ExecutorCommandKind   `json:"kind"`
	Command      string                `json:"command,omitempty"`
	Cwd          string                `json:"cwd,omitempty"`
	Payload      map[string]any        `json:"payload,omitempty"`
	Status       ExecutorCommandStatus `json:"status"`
	ExitCode     *int                  `json:"exit_code,omitempty"`
	Output       string                `json:"output,omitempty"`
	ErrorMessage string                `json:"error_message,omitempty"`
	CreatedBy    string                `json:"created_by,omitempty"`
	ExpiresAt    *time.Time            `json:"expires_at,omitempty"`
	StartedAt    *time.Time            `json:"started_at,omitempty"`
	FinishedAt   *time.Time            `json:"finished_at,omitempty"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// Normalize 清理并校验命令；exec 必须有命令，其它类型只接受受控参数。
func (c *ExecutorCommand) Normalize() error {
	if c == nil {
		return fmt.Errorf("%w: executor command is nil", ErrInvalid)
	}
	c.ExecutorID = strings.TrimSpace(c.ExecutorID)
	c.RunID = strings.TrimSpace(c.RunID)
	c.TargetID = strings.TrimSpace(c.TargetID)
	c.Command = strings.TrimSpace(c.Command)
	c.Cwd = strings.TrimSpace(c.Cwd)
	c.CreatedBy = strings.TrimSpace(c.CreatedBy)
	if c.ExecutorID == "" {
		return fmt.Errorf("%w: executor_id is required", ErrInvalid)
	}
	switch c.Kind {
	case ExecutorCommandProbe, ExecutorCommandExec, ExecutorCommandLogs, ExecutorCommandFiles:
	default:
		return fmt.Errorf("%w: unknown executor command kind %q", ErrInvalid, c.Kind)
	}
	if c.Kind == ExecutorCommandExec && c.Command == "" {
		return fmt.Errorf("%w: exec command is required", ErrInvalid)
	}
	if c.Status == "" {
		c.Status = ExecutorCommandQueued
	}
	if c.Payload == nil {
		c.Payload = map[string]any{}
	}
	return nil
}

// Terminal 表示命令已经结束，调用方不需要继续轮询。
func (c *ExecutorCommand) Terminal() bool {
	if c == nil {
		return true
	}
	switch c.Status {
	case ExecutorCommandDone, ExecutorCommandFailed, ExecutorCommandExpired, ExecutorCommandCancelled:
		return true
	}
	return false
}
