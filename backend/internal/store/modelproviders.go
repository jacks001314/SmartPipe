package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ModelProvider 是一个可连接的模型端点：Base URL、协议和加密凭据的引用。
// 模型清单挂在 provider_models 下，由目录刷新或手工添加维护。
type ModelProvider struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	BaseURL         string          `json:"base_url"`
	WireAPI         string          `json:"wire_api"`
	CredentialID    string          `json:"credential_id"`
	Enabled         bool            `json:"enabled"`
	LastTestedAt    *time.Time      `json:"last_tested_at"`
	LastTestOK      *bool           `json:"last_test_ok"`
	LastTestMessage string          `json:"last_test_message"`
	Models          []ProviderModel `json:"models"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// ProviderModel 是某个供应商目录下的一项模型。
type ProviderModel struct {
	ProviderID string    `json:"provider_id"`
	ModelID    string    `json:"model_id"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const modelProviderColumns = `id, name, base_url, wire_api, credential_id, enabled,
    last_tested_at, last_test_ok, last_test_message, created_at, updated_at`

var wireAPIs = map[string]bool{"chat": true, "responses": true}

func scanModelProvider(row interface{ Scan(...any) error }) (*ModelProvider, error) {
	var (
		p      ModelProvider
		tested sql.NullTime
		testOK sql.NullBool
	)
	err := row.Scan(&p.ID, &p.Name, &p.BaseURL, &p.WireAPI, &p.CredentialID, &p.Enabled,
		&tested, &testOK, &p.LastTestMessage, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if tested.Valid {
		v := tested.Time
		p.LastTestedAt = &v
	}
	if testOK.Valid {
		v := testOK.Bool
		p.LastTestOK = &v
	}
	p.Models = []ProviderModel{}
	return &p, nil
}

func normalizeModelProvider(p *ModelProvider) error {
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	p.WireAPI = strings.ToLower(strings.TrimSpace(p.WireAPI))
	if p.Name == "" {
		return errors.New("store: model provider name is required")
	}
	if !wireAPIs[p.WireAPI] {
		if p.WireAPI == "" {
			p.WireAPI = "chat"
		} else {
			return fmt.Errorf("store: unsupported wire api %q", p.WireAPI)
		}
	}
	return nil
}

// CreateModelProvider 新增一个模型供应商。
func (s *Store) CreateModelProvider(ctx context.Context, p *ModelProvider) (*ModelProvider, error) {
	if p == nil {
		return nil, errors.New("store: nil model provider")
	}
	if err := normalizeModelProvider(p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		p.ID = newID("mpr")
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO model_providers (id, name, base_url, wire_api, credential_id, enabled)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING `+modelProviderColumns,
		p.ID, p.Name, p.BaseURL, p.WireAPI, p.CredentialID, p.Enabled)
	out, err := scanModelProvider(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: model provider %q already exists", ErrConflict, p.Name)
		}
		return nil, fmt.Errorf("store: create model provider: %w", err)
	}
	return out, nil
}

// GetModelProvider 读取供应商及其模型目录。
func (s *Store) GetModelProvider(ctx context.Context, id string) (*ModelProvider, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+modelProviderColumns+` FROM model_providers WHERE id = $1`, id)
	p, err := scanModelProvider(row)
	if err != nil {
		return nil, err
	}
	models, err := s.ListProviderModels(ctx, id)
	if err != nil {
		return nil, err
	}
	p.Models = models
	return p, nil
}

// ListModelProviders 返回全部供应商及其模型目录。
func (s *Store) ListModelProviders(ctx context.Context) ([]ModelProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+modelProviderColumns+` FROM model_providers ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list model providers: %w", err)
	}
	defer rows.Close()

	out := make([]ModelProvider, 0, 8)
	for rows.Next() {
		p, err := scanModelProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		models, err := s.ListProviderModels(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Models = models
	}
	return out, nil
}

// ListProviderModels 返回一个供应商下的模型，按模型 ID 排序。
func (s *Store) ListProviderModels(ctx context.Context, providerID string) ([]ProviderModel, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider_id, model_id, enabled, created_at, updated_at
  FROM provider_models WHERE provider_id = $1 ORDER BY model_id`, providerID)
	if err != nil {
		return nil, fmt.Errorf("store: list provider models: %w", err)
	}
	defer rows.Close()

	out := make([]ProviderModel, 0, 8)
	for rows.Next() {
		var m ProviderModel
		if err := rows.Scan(&m.ProviderID, &m.ModelID, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateModelProvider 覆盖连接字段。凭据值不在这里，轮换走凭据接口。
func (s *Store) UpdateModelProvider(ctx context.Context, p *ModelProvider) (*ModelProvider, error) {
	if p == nil || strings.TrimSpace(p.ID) == "" {
		return nil, errors.New("store: model provider id is required")
	}
	if err := normalizeModelProvider(p); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE model_providers SET
    name = $2, base_url = $3, wire_api = $4, credential_id = $5,
    enabled = $6, updated_at = now()
WHERE id = $1
RETURNING `+modelProviderColumns,
		p.ID, p.Name, p.BaseURL, p.WireAPI, p.CredentialID, p.Enabled)
	out, err := scanModelProvider(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: model provider %q already exists", ErrConflict, p.Name)
		}
		return nil, fmt.Errorf("store: update model provider: %w", err)
	}
	return s.GetModelProvider(ctx, out.ID)
}

// SetModelProviderEnabled 启停供应商。
func (s *Store) SetModelProviderEnabled(ctx context.Context, id string, enabled bool) (*ModelProvider, error) {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE model_providers SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled); err != nil {
		return nil, fmt.Errorf("store: set model provider enabled: %w", err)
	}
	return s.GetModelProvider(ctx, id)
}

// DeleteModelProvider 删除供应商及其模型目录；凭据保留，便于审计或复用。
func (s *Store) DeleteModelProvider(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete model provider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_models WHERE provider_id = $1`, id); err != nil {
		return fmt.Errorf("store: delete provider models: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET codex_model_provider = '', codex_model = '' WHERE codex_model_provider = $1`, id); err != nil {
		return fmt.Errorf("store: unbind project model: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM model_providers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete model provider: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.clearModelDefaultIf(ctx, id, "")
	return nil
}

// clearModelDefaultIf 在供应商或模型被删/停用时清掉指向它的默认模型设置。
func (s *Store) clearModelDefaultIf(ctx context.Context, providerID, modelID string) {
	var provider, model string
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM runtime_settings WHERE key = 'codex_default_provider'`).Scan(&provider)
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM runtime_settings WHERE key = 'codex_default_model'`).Scan(&model)
	if provider != providerID || providerID == "" {
		return
	}
	if modelID != "" && model != modelID {
		return
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM runtime_settings WHERE key IN ('codex_default_provider','codex_default_model','codex_default_effort')`)
}

// AddProviderModel 手工添加或更新一个模型项。
func (s *Store) AddProviderModel(ctx context.Context, providerID, modelID string, enabled bool) (*ProviderModel, error) {
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return nil, errors.New("store: provider and model id are required")
	}
	var m ProviderModel
	err := s.db.QueryRowContext(ctx, `
INSERT INTO provider_models (provider_id, model_id, enabled)
VALUES ($1,$2,$3)
ON CONFLICT (provider_id, model_id)
DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()
RETURNING provider_id, model_id, enabled, created_at, updated_at`,
		providerID, modelID, enabled).
		Scan(&m.ProviderID, &m.ModelID, &m.Enabled, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: add provider model: %w", err)
	}
	return &m, nil
}

// UpsertCatalogModel 用于目录刷新：新模型默认停用，已有模型保持原启停状态。
func (s *Store) UpsertCatalogModel(ctx context.Context, providerID, modelID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO provider_models (provider_id, model_id, enabled)
VALUES ($1,$2,false)
ON CONFLICT (provider_id, model_id) DO NOTHING`, providerID, modelID)
	if err != nil {
		return fmt.Errorf("store: upsert catalog model: %w", err)
	}
	return nil
}

// SetProviderModelEnabled 启停目录里的一个模型。
func (s *Store) SetProviderModelEnabled(ctx context.Context, providerID, modelID string, enabled bool) (*ProviderModel, error) {
	var m ProviderModel
	err := s.db.QueryRowContext(ctx, `
UPDATE provider_models SET enabled = $3, updated_at = now()
 WHERE provider_id = $1 AND model_id = $2
RETURNING provider_id, model_id, enabled, created_at, updated_at`,
		providerID, modelID, enabled).
		Scan(&m.ProviderID, &m.ModelID, &m.Enabled, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: set provider model enabled: %w", err)
	}
	if !enabled {
		_, _ = s.db.ExecContext(ctx, `UPDATE projects SET codex_model = '' WHERE codex_model_provider = $1 AND codex_model = $2`, providerID, modelID)
		s.clearModelDefaultIf(ctx, providerID, modelID)
	}
	return &m, nil
}

// DeleteProviderModel 从目录里删除一个模型。
func (s *Store) DeleteProviderModel(ctx context.Context, providerID, modelID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM provider_models WHERE provider_id = $1 AND model_id = $2`, providerID, modelID)
	if err != nil {
		return fmt.Errorf("store: delete provider model: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE projects SET codex_model = '' WHERE codex_model_provider = $1 AND codex_model = $2`, providerID, modelID)
	s.clearModelDefaultIf(ctx, providerID, modelID)
	return nil
}

// RecordModelProviderTest 记录一次供应商连通性测试。
func (s *Store) RecordModelProviderTest(ctx context.Context, id string, ok bool, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE model_providers SET last_tested_at = $2, last_test_ok = $3, last_test_message = $4, updated_at = now()
 WHERE id = $1`, id, time.Now().UTC(), ok, message)
	if err != nil {
		return fmt.Errorf("store: record provider test: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
