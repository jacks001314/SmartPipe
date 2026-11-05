package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ToolCall 是一次 MCP 工具调用记录。
type ToolCall struct {
	ID          int64           `json:"id"`
	ProjectID   *string         `json:"project_id"`
	TokenID     string          `json:"token_id"`
	AgentName   string          `json:"agent_name"`
	Role        string          `json:"role"`
	Tool        string          `json:"tool"`
	Status      string          `json:"status"`
	DurationMS  int             `json:"duration_ms"`
	ArgsSummary json.RawMessage `json:"args_summary"`
	Error       string          `json:"error"`
	CreatedAt   time.Time       `json:"created_at"`
}

// CreateToolCall 写入一条工具调用记录。
func (s *Store) CreateToolCall(ctx context.Context, t ToolCall) (*ToolCall, error) {
	if len(t.ArgsSummary) == 0 {
		t.ArgsSummary = json.RawMessage("{}")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO tool_calls (project_id, token_id, agent_name, role, tool, status, duration_ms, args_summary, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING id, created_at`,
		t.ProjectID, t.TokenID, t.AgentName, t.Role, t.Tool, t.Status, t.DurationMS, t.ArgsSummary, t.Error)
	if err := row.Scan(&t.ID, &t.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: create tool call: %w", err)
	}
	return &t, nil
}

// ListToolCalls 返回最近的工具调用，可按工具过滤。
func (s *Store) ListToolCalls(ctx context.Context, tool string, limit int) ([]ToolCall, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, project_id, token_id, agent_name, role, tool, status, duration_ms, args_summary, error, created_at
 FROM tool_calls WHERE ($1 = '' OR tool = $1) ORDER BY id DESC LIMIT $2`
	rows, err := s.db.QueryContext(ctx, query, tool, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list tool calls: %w", err)
	}
	defer rows.Close()
	out := make([]ToolCall, 0, limit)
	for rows.Next() {
		var (
			t       ToolCall
			project sql.NullString
		)
		if err := rows.Scan(&t.ID, &project, &t.TokenID, &t.AgentName, &t.Role, &t.Tool, &t.Status,
			&t.DurationMS, &t.ArgsSummary, &t.Error, &t.CreatedAt); err != nil {
			return nil, err
		}
		if project.Valid {
			v := project.String
			t.ProjectID = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetSetting 读取运行时设置；不存在返回空串。
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

// SetSetting 写入运行时设置。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runtime_settings (key, value, updated_at) VALUES ($1,$2,now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	if err != nil {
		return fmt.Errorf("store: set setting: %w", err)
	}
	return nil
}
