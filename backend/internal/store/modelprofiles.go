package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ModelProfile 是一个可复用的“供应商 + 模型 + 推理强度”组合。
type ModelProfile struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	ProviderID      string    `json:"provider_id"`
	ProviderName    string    `json:"provider_name"`
	Model           string    `json:"model"`
	ReasoningEffort string    `json:"reasoning_effort"`
	Enabled         bool      `json:"enabled"`
	Source          string    `json:"source"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

const modelProfileSelectColumns = `p.id, p.name, p.provider_id, COALESCE(m.name, ''),
    p.model_id, p.reasoning_effort, p.enabled, p.source, p.created_at, p.updated_at`

const modelProfileReturningColumns = `id, name, provider_id,
    COALESCE((SELECT name FROM model_providers WHERE id = model_profiles.provider_id), ''),
    model_id, reasoning_effort, enabled, source, created_at, updated_at`

func scanModelProfile(row interface{ Scan(...any) error }) (*ModelProfile, error) {
	var p ModelProfile
	err := row.Scan(&p.ID, &p.Name, &p.ProviderID, &p.ProviderName,
		&p.Model, &p.ReasoningEffort, &p.Enabled, &p.Source, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func normalizeModelProfile(p *ModelProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	p.ProviderID = strings.TrimSpace(p.ProviderID)
	p.Model = strings.TrimSpace(p.Model)
	p.ReasoningEffort = strings.ToLower(strings.TrimSpace(p.ReasoningEffort))
	if p.Name == "" {
		return errors.New("store: model profile name is required")
	}
	if p.ProviderID == "" {
		return errors.New("store: model profile provider is required")
	}
	if p.Model == "" {
		return errors.New("store: model profile model is required")
	}
	return nil
}

// UpsertModelProfile 按不区分大小写的名称创建或更新 Profile。
func (s *Store) UpsertModelProfile(ctx context.Context, p *ModelProfile) (*ModelProfile, error) {
	if p == nil {
		return nil, errors.New("store: nil model profile")
	}
	if err := normalizeModelProfile(p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		p.ID = newID("mprf")
	}
	if p.Source == "" {
		p.Source = "manual"
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO model_profiles (id, name, provider_id, model_id, reasoning_effort, enabled, source)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (lower(name)) DO UPDATE SET
    provider_id = EXCLUDED.provider_id,
    model_id = EXCLUDED.model_id,
    reasoning_effort = EXCLUDED.reasoning_effort,
    enabled = EXCLUDED.enabled,
    source = EXCLUDED.source,
    updated_at = now()
RETURNING `+modelProfileReturningColumns,
		p.ID, p.Name, p.ProviderID, p.Model, p.ReasoningEffort, p.Enabled, p.Source)
	out, err := scanModelProfile(row)
	if err != nil {
		return nil, fmt.Errorf("store: upsert model profile: %w", err)
	}
	return out, nil
}

// GetModelProfile 读取单个 Profile。
func (s *Store) GetModelProfile(ctx context.Context, id string) (*ModelProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+modelProfileSelectColumns+`
  FROM model_profiles p LEFT JOIN model_providers m ON m.id = p.provider_id
 WHERE p.id = $1`, id)
	return scanModelProfile(row)
}

// ListModelProfiles 返回全部 Profile 及其供应商名称。
func (s *Store) ListModelProfiles(ctx context.Context) ([]ModelProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+modelProfileSelectColumns+`
  FROM model_profiles p LEFT JOIN model_providers m ON m.id = p.provider_id
 ORDER BY p.name`)
	if err != nil {
		return nil, fmt.Errorf("store: list model profiles: %w", err)
	}
	defer rows.Close()
	out := make([]ModelProfile, 0, 8)
	for rows.Next() {
		item, err := scanModelProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// SetModelProfileEnabled 启停 Profile；停用时清理指向同一组合的默认设置。
func (s *Store) SetModelProfileEnabled(ctx context.Context, id string, enabled bool) (*ModelProfile, error) {
	row := s.db.QueryRowContext(ctx, `
UPDATE model_profiles SET enabled = $2, updated_at = now()
 WHERE id = $1
RETURNING `+modelProfileReturningColumns, id, enabled)
	out, err := scanModelProfile(row)
	if err != nil {
		return nil, fmt.Errorf("store: set model profile enabled: %w", err)
	}
	if !enabled {
		s.clearModelDefaultIf(ctx, out.ProviderID, out.Model)
	}
	return out, nil
}

// DeleteModelProfile 删除一个 Profile。
func (s *Store) DeleteModelProfile(ctx context.Context, id string) error {
	profile, err := s.GetModelProfile(ctx, id)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM model_profiles WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete model profile: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	s.clearModelDefaultIf(ctx, profile.ProviderID, profile.Model)
	return nil
}
