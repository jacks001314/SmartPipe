package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AgentToken 是发给 Agent（lead/dev/test/release）的受控令牌，只保存哈希。
type AgentToken struct {
	ID         string     `json:"id"`
	ProjectID  *string    `json:"project_id"`
	Role       string     `json:"role"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

const agentTokenColumns = `id, project_id, role, name, prefix, enabled, last_used_at, expires_at, created_at`

func scanAgentToken(row interface{ Scan(...any) error }) (*AgentToken, error) {
	var (
		t        AgentToken
		project  sql.NullString
		lastUsed sql.NullTime
		expires  sql.NullTime
	)
	err := row.Scan(&t.ID, &project, &t.Role, &t.Name, &t.Prefix, &t.Enabled, &lastUsed, &expires, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if project.Valid {
		v := project.String
		t.ProjectID = &v
	}
	if lastUsed.Valid {
		v := lastUsed.Time
		t.LastUsedAt = &v
	}
	if expires.Valid {
		v := expires.Time
		t.ExpiresAt = &v
	}
	return &t, nil
}

// HashAgentToken 返回令牌的 sha256 十六进制摘要。
func HashAgentToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// CreateAgentToken 生成令牌并落库；明文只在返回值中出现一次。
func (s *Store) CreateAgentToken(ctx context.Context, t AgentToken) (*AgentToken, string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("store: generate token: %w", err)
	}
	token := "spt_" + hex.EncodeToString(raw)
	t.Prefix = token[:12]
	if strings.TrimSpace(t.Role) == "" {
		t.Role = "lead"
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO agent_tokens (project_id, role, name, prefix, token_hash, expires_at)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id, enabled, created_at`,
		t.ProjectID, t.Role, t.Name, t.Prefix, HashAgentToken(token), t.ExpiresAt)
	if err := row.Scan(&t.ID, &t.Enabled, &t.CreatedAt); err != nil {
		return nil, "", fmt.Errorf("store: create agent token: %w", err)
	}
	return &t, token, nil
}

// ListAgentTokens 返回令牌（不含哈希）。
func (s *Store) ListAgentTokens(ctx context.Context, projectID *string) ([]AgentToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentTokenColumns+` FROM agent_tokens
 WHERE $1::text IS NULL OR project_id = $1 ORDER BY created_at DESC LIMIT 200`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list agent tokens: %w", err)
	}
	defer rows.Close()
	out := make([]AgentToken, 0, 16)
	for rows.Next() {
		t, err := scanAgentToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// RevokeAgentToken 停用令牌。
func (s *Store) RevokeAgentToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_tokens SET enabled = FALSE WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: revoke agent token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// VerifyAgentToken 校验明文令牌并刷新最近使用时间。
func (s *Store) VerifyAgentToken(ctx context.Context, token string) (*AgentToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentTokenColumns+` FROM agent_tokens
 WHERE token_hash = $1 AND enabled AND (expires_at IS NULL OR expires_at > now())`, HashAgentToken(token))
	t, err := scanAgentToken(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: verify agent token: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE agent_tokens SET last_used_at = now() WHERE id = $1`, t.ID)
	return t, nil
}
