package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ProjectBinding 把一个项目绑定到一个 App Server 上的 pipeline-lead。
// 桥接收到新 Issue 后按这张表找到唯一的对外负责人。
type ProjectBinding struct {
	ProjectID     string `json:"project_id"`
	AppServerID   string `json:"app_server_id"`
	AppServerAddr string `json:"app_server_addr"`
	// AgentProjectID 是 lead 在 Syntropy 里的项目归属，投递消息必须带上它。
	AgentProjectID string    `json:"agent_project_id"`
	LeadAgentID    string    `json:"lead_agent_id"`
	LeadAgentName  string    `json:"lead_agent_name"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Dispatch 是一次向 pipeline-lead 投递任务的记录。
type Dispatch struct {
	ID      int64     `json:"id"`
	LinkID  int64     `json:"link_id"`
	AgentID string    `json:"agent_id"`
	Status  string    `json:"status"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// UpsertBinding 新建或更新项目绑定。
func (s *Store) UpsertBinding(ctx context.Context, b ProjectBinding) (*ProjectBinding, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO project_bindings (project_id, app_server_id, app_server_addr, agent_project_id, lead_agent_id, lead_agent_name, updated_at)
VALUES ($1,$2,$3,$4,$5,$6, now())
ON CONFLICT (project_id) DO UPDATE SET
    app_server_id = EXCLUDED.app_server_id,
    app_server_addr = EXCLUDED.app_server_addr,
    agent_project_id = EXCLUDED.agent_project_id,
    lead_agent_id = EXCLUDED.lead_agent_id,
    lead_agent_name = EXCLUDED.lead_agent_name,
    updated_at = now()
RETURNING project_id, app_server_id, app_server_addr, agent_project_id, lead_agent_id, lead_agent_name, updated_at`,
		b.ProjectID, b.AppServerID, b.AppServerAddr, b.AgentProjectID, b.LeadAgentID, b.LeadAgentName)
	var out ProjectBinding
	if err := row.Scan(&out.ProjectID, &out.AppServerID, &out.AppServerAddr, &out.AgentProjectID, &out.LeadAgentID, &out.LeadAgentName, &out.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store: upsert binding: %w", err)
	}
	return &out, nil
}

// GetBinding 读取项目绑定；不存在返回 ErrNotFound。
func (s *Store) GetBinding(ctx context.Context, projectID string) (*ProjectBinding, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT project_id, app_server_id, app_server_addr, agent_project_id, lead_agent_id, lead_agent_name, updated_at
  FROM project_bindings WHERE project_id = $1`, projectID)
	var b ProjectBinding
	err := row.Scan(&b.ProjectID, &b.AppServerID, &b.AppServerAddr, &b.AgentProjectID, &b.LeadAgentID, &b.LeadAgentName, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get binding: %w", err)
	}
	return &b, nil
}

// ListBindings 返回全部项目绑定。
func (s *Store) ListBindings(ctx context.Context) ([]ProjectBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, app_server_id, app_server_addr, agent_project_id, lead_agent_id, lead_agent_name, updated_at
  FROM project_bindings ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list bindings: %w", err)
	}
	defer rows.Close()
	out := make([]ProjectBinding, 0, 8)
	for rows.Next() {
		var b ProjectBinding
		if err := rows.Scan(&b.ProjectID, &b.AppServerID, &b.AppServerAddr, &b.AgentProjectID, &b.LeadAgentID, &b.LeadAgentName, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBinding 解除绑定。
func (s *Store) DeleteBinding(ctx context.Context, projectID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM project_bindings WHERE project_id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("store: delete binding: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordDispatch 记录一次投递结果。
func (s *Store) RecordDispatch(ctx context.Context, d Dispatch) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO bridge_dispatches (link_id, agent_id, status, message) VALUES ($1,$2,$3,$4)`,
		d.LinkID, d.AgentID, d.Status, d.Message); err != nil {
		return fmt.Errorf("store: record dispatch: %w", err)
	}
	return nil
}

// ListDispatches 返回最近投递记录。
func (s *Store) ListDispatches(ctx context.Context, limit int) ([]Dispatch, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, link_id, agent_id, status, message, at FROM bridge_dispatches ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list dispatches: %w", err)
	}
	defer rows.Close()
	out := make([]Dispatch, 0, limit)
	for rows.Next() {
		var d Dispatch
		if err := rows.Scan(&d.ID, &d.LinkID, &d.AgentID, &d.Status, &d.Message, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListWebhookDeliveries 返回最近收到的 webhook 投递。
type WebhookDelivery struct {
	DeliveryID string    `json:"delivery_id"`
	Event      string    `json:"event"`
	Repo       string    `json:"repo"`
	ReceivedAt time.Time `json:"received_at"`
}

func (s *Store) ListWebhookDeliveries(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT delivery_id, event, repo, received_at FROM webhook_deliveries ORDER BY received_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list webhook deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]WebhookDelivery, 0, limit)
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.DeliveryID, &d.Event, &d.Repo, &d.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
