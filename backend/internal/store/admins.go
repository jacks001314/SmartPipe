package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AdminUser 是配置台的登录账号。PasswordHash 永不出现在 JSON 里。
type AdminUser struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	DisplayName  string     `json:"display_name"`
	Enabled      bool       `json:"enabled"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// AdminSession 是一次登录会话；ID 存的是 Cookie 令牌的 SHA-256。
type AdminSession struct {
	ID          string    `json:"-"`
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

const adminUserColumns = `id, username, password_hash, display_name, enabled,
    last_login_at, created_at, updated_at`

func scanAdminUser(row interface{ Scan(...any) error }) (*AdminUser, error) {
	var (
		u    AdminUser
		last sql.NullTime
	)
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Enabled,
		&last, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if last.Valid {
		v := last.Time
		u.LastLoginAt = &v
	}
	return &u, nil
}

// CountAdminUsers 返回账号数量，用于首次启动时决定是否创建管理员。
func (s *Store) CountAdminUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM admin_users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count admin users: %w", err)
	}
	return n, nil
}

// CreateAdminUser 新增一个登录账号。
func (s *Store) CreateAdminUser(ctx context.Context, username, passwordHash, displayName string) (*AdminUser, error) {
	username = strings.TrimSpace(username)
	if username == "" || passwordHash == "" {
		return nil, errors.New("store: admin username and password hash are required")
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = username
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO admin_users (id, username, password_hash, display_name)
VALUES ($1,$2,$3,$4)
RETURNING `+adminUserColumns,
		newID("usr"), username, passwordHash, displayName)
	u, err := scanAdminUser(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: admin user %q already exists", ErrConflict, username)
		}
		return nil, fmt.Errorf("store: create admin user: %w", err)
	}
	return u, nil
}

// GetAdminUserByUsername 按用户名读取账号（含密码哈希，仅供登录校验）。
func (s *Store) GetAdminUserByUsername(ctx context.Context, username string) (*AdminUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+adminUserColumns+` FROM admin_users WHERE username = $1`, strings.TrimSpace(username))
	return scanAdminUser(row)
}

// UpdateAdminPassword 更新密码哈希。
func (s *Store) UpdateAdminPassword(ctx context.Context, id, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, passwordHash)
	if err != nil {
		return fmt.Errorf("store: update admin password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAdminLogin 记录最近登录时间。
func (s *Store) TouchAdminLogin(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET last_login_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}

// CreateAdminSession 保存会话（id 为令牌哈希）。
func (s *Store) CreateAdminSession(ctx context.Context, id, userID string, expiresAt time.Time, userAgent, ip string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO admin_sessions (id, user_id, expires_at, user_agent, ip)
VALUES ($1,$2,$3,$4,$5)`, id, userID, expiresAt, userAgent, ip)
	if err != nil {
		return fmt.Errorf("store: create admin session: %w", err)
	}
	return nil
}

// GetAdminSession 读取有效会话；过期或账号停用返回 ErrNotFound。
func (s *Store) GetAdminSession(ctx context.Context, id string) (*AdminSession, error) {
	var sess AdminSession
	err := s.db.QueryRowContext(ctx, `
SELECT s.id, s.user_id, u.username, u.display_name, s.expires_at, s.created_at, s.last_seen_at
  FROM admin_sessions s
  JOIN admin_users u ON u.id = s.user_id
 WHERE s.id = $1 AND s.expires_at > now() AND u.enabled`, id).
		Scan(&sess.ID, &sess.UserID, &sess.Username, &sess.DisplayName,
			&sess.ExpiresAt, &sess.CreatedAt, &sess.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get admin session: %w", err)
	}
	return &sess, nil
}

// TouchAdminSession 刷新最近活跃时间。
func (s *Store) TouchAdminSession(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE admin_sessions SET last_seen_at = now() WHERE id = $1`, id)
}

// DeleteAdminSession 退出登录。
func (s *Store) DeleteAdminSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id = $1`, id)
	return err
}

// DeleteUserSessions 在改密码后让其他会话失效。
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = $1`, userID)
	return err
}

// DeleteExpiredAdminSessions 清理过期会话。
func (s *Store) DeleteExpiredAdminSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}
