package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"smartpipe/backend/internal/secretbox"
)

// 支持的凭据类型。新增类型时必须同时补连接测试实现。
const (
	CredentialGitHubApp  = "github_app"
	CredentialGitHubPAT  = "github_pat"
	CredentialGitHubHook = "github_webhook_secret"
	CredentialLanxin     = "lanxin"
	CredentialSyntropy   = "syntropy"
	CredentialGitSSH     = "git_ssh"
	CredentialSigningKey = "signing_key"
	// CredentialModelProvider 是模型网关的 API Key，供模型档案引用。
	CredentialModelProvider = "model_provider"
)

var allowedKinds = map[string]bool{
	CredentialGitHubApp:     true,
	CredentialGitHubPAT:     true,
	CredentialGitHubHook:    true,
	CredentialLanxin:        true,
	CredentialSyntropy:      true,
	CredentialGitSSH:        true,
	CredentialSigningKey:    true,
	CredentialModelProvider: true,
}

// ValidKind 判断凭据类型是否受支持。
func ValidKind(kind string) bool { return allowedKinds[kind] }

const credentialColumns = `id, project_id, kind, name, mask, metadata, enabled,
    last_tested_at, last_test_ok, last_test_message, created_at, updated_at`

func scanCredential(row interface{ Scan(...any) error }) (*Credential, error) {
	var (
		c       Credential
		project sql.NullString
		rawMeta []byte
		tested  sql.NullTime
		testOK  sql.NullBool
	)
	err := row.Scan(&c.ID, &project, &c.Kind, &c.Name, &c.Mask, &rawMeta, &c.Enabled,
		&tested, &testOK, &c.LastTestMessage, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if project.Valid {
		v := project.String
		c.ProjectID = &v
	}
	if len(rawMeta) > 0 {
		if err := json.Unmarshal(rawMeta, &c.Metadata); err != nil {
			return nil, fmt.Errorf("store: decode credential metadata: %w", err)
		}
	}
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	if tested.Valid {
		v := tested.Time
		c.LastTestedAt = &v
	}
	if testOK.Valid {
		v := testOK.Bool
		c.LastTestOK = &v
	}
	return &c, nil
}

// CreateCredential 加密并保存一条凭据。密文与明文都不会出现在返回值里。
func (s *Store) CreateCredential(ctx context.Context, projectID *string, kind, name, plaintext string, metadata map[string]any) (*Credential, error) {
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	if !ValidKind(kind) {
		return nil, fmt.Errorf("store: unsupported credential kind %q", kind)
	}
	if name == "" {
		return nil, errors.New("store: credential name is required")
	}
	if plaintext == "" {
		return nil, errors.New("store: credential value is required")
	}
	sealed, err := s.box.Seal(plaintext)
	if err != nil {
		return nil, fmt.Errorf("store: seal credential: %w", err)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	rawMeta, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("store: encode metadata: %w", err)
	}
	id := newID("cred")
	row := s.db.QueryRowContext(ctx, `
INSERT INTO credentials (id, project_id, kind, name, ciphertext, mask, metadata)
VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
RETURNING `+credentialColumns,
		id, projectID, kind, name, sealed, secretbox.Mask(plaintext), string(rawMeta))
	c, err := scanCredential(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: credential %s/%s already exists in this scope", ErrConflict, kind, name)
		}
		return nil, fmt.Errorf("store: create credential: %w", err)
	}
	return c, nil
}

// ListCredentials 返回凭据视图，可按项目过滤（nil 表示全部）。
func (s *Store) ListCredentials(ctx context.Context, projectID *string) ([]Credential, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if projectID == nil {
		rows, err = s.db.QueryContext(ctx, `SELECT `+credentialColumns+` FROM credentials ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT `+credentialColumns+` FROM credentials WHERE project_id = $1 ORDER BY created_at DESC`, *projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list credentials: %w", err)
	}
	defer rows.Close()

	out := make([]Credential, 0, 8)
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetSecret 解密凭据明文。只允许服务端工具调用，禁止从管理 API 暴露。
func (s *Store) GetSecret(ctx context.Context, id string) (kind string, plaintext string, metadata map[string]any, err error) {
	var sealed string
	var rawMeta []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT kind, ciphertext, metadata FROM credentials WHERE id = $1 AND enabled`, id).
		Scan(&kind, &sealed, &rawMeta)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, ErrNotFound
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("store: get secret: %w", err)
	}
	plaintext, err = s.box.Open(sealed)
	if err != nil {
		return "", "", nil, err
	}
	metadata = map[string]any{}
	if len(rawMeta) > 0 {
		_ = json.Unmarshal(rawMeta, &metadata)
	}
	return kind, plaintext, metadata, nil
}

// RotateCredential 用新值替换凭据，旧值立即失效。
func (s *Store) RotateCredential(ctx context.Context, id, plaintext string) (*Credential, error) {
	if plaintext == "" {
		return nil, errors.New("store: credential value is required")
	}
	sealed, err := s.box.Seal(plaintext)
	if err != nil {
		return nil, fmt.Errorf("store: seal credential: %w", err)
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE credentials
   SET ciphertext = $2, mask = $3, last_tested_at = NULL, last_test_ok = NULL,
       last_test_message = '', updated_at = now()
 WHERE id = $1
RETURNING `+credentialColumns, id, sealed, secretbox.Mask(plaintext))
	c, err := scanCredential(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SetCredentialEnabled 启停一条凭据。
func (s *Store) SetCredentialEnabled(ctx context.Context, id string, enabled bool) (*Credential, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE credentials SET enabled = $2, updated_at = now() WHERE id = $1 RETURNING `+credentialColumns, id, enabled)
	return scanCredential(row)
}

// DeleteCredential 删除凭据。
func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete credential: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordCredentialTest 记录一次连接测试结果。
func (s *Store) RecordCredentialTest(ctx context.Context, id string, ok bool, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE credentials SET last_tested_at = $2, last_test_ok = $3, last_test_message = $4, updated_at = now()
 WHERE id = $1`, id, time.Now().UTC(), ok, message)
	if err != nil {
		return fmt.Errorf("store: record credential test: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
