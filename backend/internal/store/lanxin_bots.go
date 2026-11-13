package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LanxinBot 是一个蓝信个人机器人配置。密钥仍存放在 credentials 表中。
type LanxinBot struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	CredentialID    string     `json:"credential_id"`
	AppID           string     `json:"app_id"`
	APIBase         string     `json:"api_base"`
	RequireAtMe     bool       `json:"require_at_me"`
	Enabled         bool       `json:"enabled"`
	Status          string     `json:"status"`
	LastError       string     `json:"last_error"`
	LastConnectedAt *time.Time `json:"last_connected_at"`
	LastGroupSyncAt *time.Time `json:"last_group_sync_at"`
	LastSyncError   string     `json:"last_sync_error"`
	GroupCount      int        `json:"group_count"`
	MessageCount    int        `json:"message_count"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// LanxinGroup 是机器人所在群的最新快照；active=false 表示最近一次同步已不可见。
type LanxinGroup struct {
	ID            string    `json:"id"`
	BotID         string    `json:"bot_id"`
	BotName       string    `json:"bot_name"`
	GroupID       string    `json:"group_id"`
	Name          string    `json:"name"`
	AvatarURL     string    `json:"avatar_url"`
	Description   string    `json:"description"`
	OwnerID       string    `json:"owner_id"`
	OwnerName     string    `json:"owner_name"`
	MemberCount   int       `json:"member_count"`
	SendMsgStatus bool      `json:"send_msg_status"`
	Active        bool      `json:"active"`
	SyncedAt      time.Time `json:"synced_at"`
	ProjectID     *string   `json:"project_id,omitempty"`
	ProjectName   string    `json:"project_name,omitempty"`

	// ResponseMode 是群响应策略：at_only / smart / all。
	ResponseMode         string  `json:"response_mode"`
	ShadowMode           bool    `json:"shadow_mode"`
	ReplyCooldownSeconds int     `json:"reply_cooldown_seconds"`
	HourlyReplyLimit     int     `json:"hourly_reply_limit"`
	AutoIssueEnabled     bool    `json:"auto_issue_enabled"`
	AutoIssueConfidence  float64 `json:"auto_issue_confidence"`
	DailyAutoIssueLimit  int     `json:"daily_auto_issue_limit"`
	AnswerConfidence     float64 `json:"answer_confidence"`
}

const lanxinBotColumns = `b.id, b.name, b.credential_id, b.app_id, b.api_base,
    b.require_at_me, b.enabled, b.status, b.last_error, b.last_connected_at,
    b.last_group_sync_at, b.last_sync_error, b.created_at, b.updated_at`

func scanLanxinBot(row interface{ Scan(...any) error }) (*LanxinBot, error) {
	var item LanxinBot
	var connected, groupSync sql.NullTime
	err := row.Scan(&item.ID, &item.Name, &item.CredentialID, &item.AppID, &item.APIBase,
		&item.RequireAtMe, &item.Enabled, &item.Status, &item.LastError, &connected,
		&groupSync, &item.LastSyncError, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if connected.Valid {
		v := connected.Time
		item.LastConnectedAt = &v
	}
	if groupSync.Valid {
		v := groupSync.Time
		item.LastGroupSyncAt = &v
	}
	return &item, nil
}

func (s *Store) ListLanxinBots(ctx context.Context) ([]LanxinBot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+lanxinBotColumns+`,
        (SELECT count(*) FROM lanxin_groups g WHERE g.bot_id=b.id AND g.active),
        (SELECT count(*) FROM lanxin_messages m WHERE m.bot_id=b.id)
      FROM lanxin_bots b ORDER BY b.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list lanxin bots: %w", err)
	}
	defer rows.Close()
	out := make([]LanxinBot, 0, 4)
	for rows.Next() {
		var item LanxinBot
		var connected, groupSync sql.NullTime
		var groups, messages int
		if err := rows.Scan(&item.ID, &item.Name, &item.CredentialID, &item.AppID, &item.APIBase,
			&item.RequireAtMe, &item.Enabled, &item.Status, &item.LastError, &connected,
			&groupSync, &item.LastSyncError, &item.CreatedAt, &item.UpdatedAt, &groups, &messages); err != nil {
			return nil, fmt.Errorf("store: scan lanxin bot: %w", err)
		}
		if connected.Valid {
			v := connected.Time
			item.LastConnectedAt = &v
		}
		if groupSync.Valid {
			v := groupSync.Time
			item.LastGroupSyncAt = &v
		}
		item.GroupCount = groups
		item.MessageCount = messages
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetLanxinBot(ctx context.Context, id string) (*LanxinBot, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+lanxinBotColumns+` FROM lanxin_bots b WHERE b.id=$1`, strings.TrimSpace(id))
	item, err := scanLanxinBot(row)
	if err != nil {
		return nil, err
	}
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM lanxin_groups WHERE bot_id=$1 AND active`, item.ID).Scan(&item.GroupCount)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM lanxin_messages WHERE bot_id=$1`, item.ID).Scan(&item.MessageCount)
	return item, nil
}

func (s *Store) CreateLanxinBot(ctx context.Context, item LanxinBot) (*LanxinBot, error) {
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || item.CredentialID == "" {
		return nil, errors.New("store: lanxin bot name and credential are required")
	}
	if item.ID == "" {
		item.ID = newID("lbot")
	}
	if item.APIBase == "" {
		item.APIBase = "https://open.e.lanxin.cn/open/apigw"
	}
	if item.Status == "" {
		item.Status = "disconnected"
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO lanxin_bots AS b (id, name, credential_id, app_id, api_base, require_at_me, enabled, status)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING `+lanxinBotColumns, item.ID, item.Name, item.CredentialID, item.AppID, item.APIBase,
		item.RequireAtMe, item.Enabled, item.Status)
	out, err := scanLanxinBot(row)
	if err != nil {
		return nil, fmt.Errorf("store: create lanxin bot: %w", err)
	}
	return out, nil
}

func (s *Store) UpdateLanxinBot(ctx context.Context, item *LanxinBot) (*LanxinBot, error) {
	if item == nil || item.ID == "" {
		return nil, errors.New("store: lanxin bot id is required")
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE lanxin_bots AS b SET name=$2, app_id=$3, api_base=$4, require_at_me=$5,
    enabled=$6, updated_at=now() WHERE id=$1
RETURNING `+lanxinBotColumns, item.ID, strings.TrimSpace(item.Name), item.AppID, item.APIBase,
		item.RequireAtMe, item.Enabled)
	out, err := scanLanxinBot(row)
	if err != nil {
		return nil, fmt.Errorf("store: update lanxin bot: %w", err)
	}
	return out, nil
}

func (s *Store) SetLanxinBotEnabled(ctx context.Context, id string, enabled bool) (*LanxinBot, error) {
	row := s.db.QueryRowContext(ctx, `UPDATE lanxin_bots AS b SET enabled=$2,
        status=CASE WHEN $2 THEN status ELSE 'disabled' END, updated_at=now()
        WHERE id=$1 RETURNING `+lanxinBotColumns, id, enabled)
	item, err := scanLanxinBot(row)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) SetLanxinBotStatus(ctx context.Context, id, status, lastError string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("store: lanxin bot id is required")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "disconnected"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lanxin_bots SET status=$2, last_error=$3,
        last_connected_at=CASE WHEN $2='connected' THEN now() ELSE last_connected_at END,
        updated_at=now() WHERE id=$1`, id, status, lastError)
	if err != nil {
		return fmt.Errorf("store: set lanxin bot status: %w", err)
	}
	return nil
}

func (s *Store) SetLanxinBotSyncResult(ctx context.Context, id string, errMessage string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lanxin_bots SET last_group_sync_at=now(), last_sync_error=$2,
        updated_at=now() WHERE id=$1`, id, errMessage)
	if err != nil {
		return fmt.Errorf("store: set lanxin sync result: %w", err)
	}
	return nil
}

func (s *Store) DeleteLanxinBot(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM lanxin_bots WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("store: delete lanxin bot: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceLanxinGroups 把机器人当前可见的群写成新快照；消失的群保留历史但置为 inactive。
func (s *Store) ReplaceLanxinGroups(ctx context.Context, botID string, groups []LanxinGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE lanxin_groups SET active=false, updated_at=now() WHERE bot_id=$1`, botID); err != nil {
		return err
	}
	for _, g := range groups {
		if strings.TrimSpace(g.GroupID) == "" {
			continue
		}
		if g.ID == "" {
			g.ID = newID("lgrp")
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO lanxin_groups (id, bot_id, group_id, name, avatar_url, description, owner_id,
    owner_name, member_count, send_msg_status, active, synced_at, shadow_mode)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,now(),false)
ON CONFLICT (bot_id, group_id) DO UPDATE SET
    name=EXCLUDED.name, avatar_url=EXCLUDED.avatar_url, description=EXCLUDED.description,
    owner_id=EXCLUDED.owner_id, owner_name=EXCLUDED.owner_name,
    member_count=EXCLUDED.member_count, send_msg_status=EXCLUDED.send_msg_status,
    active=true, synced_at=now(), updated_at=now()`, g.ID, botID, g.GroupID, g.Name, g.AvatarURL,
			g.Description, g.OwnerID, g.OwnerName, g.MemberCount, g.SendMsgStatus)
		if err != nil {
			return fmt.Errorf("store: upsert lanxin group: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) ListLanxinGroups(ctx context.Context, botID string, includeInactive bool) ([]LanxinGroup, error) {
	where := "WHERE 1=1"
	args := []any{}
	if strings.TrimSpace(botID) != "" {
		args = append(args, strings.TrimSpace(botID))
		where += fmt.Sprintf(" AND g.bot_id=$%d", len(args))
	}
	if !includeInactive {
		where += " AND g.active=true"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.bot_id,b.name,g.group_id,g.name,g.avatar_url,
        g.description,g.owner_id,g.owner_name,g.member_count,g.send_msg_status,g.active,g.synced_at,
        g.response_mode,g.shadow_mode,g.reply_cooldown_seconds,g.hourly_reply_limit,
        g.auto_issue_enabled,g.auto_issue_confidence,g.daily_auto_issue_limit,g.answer_confidence
      FROM lanxin_groups g JOIN lanxin_bots b ON b.id=g.bot_id `+where+`
      ORDER BY b.name, g.name, g.group_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list lanxin groups: %w", err)
	}
	defer rows.Close()
	out := make([]LanxinGroup, 0, 32)
	for rows.Next() {
		var g LanxinGroup
		if err := rows.Scan(&g.ID, &g.BotID, &g.BotName, &g.GroupID, &g.Name, &g.AvatarURL,
			&g.Description, &g.OwnerID, &g.OwnerName, &g.MemberCount, &g.SendMsgStatus,
			&g.Active, &g.SyncedAt, &g.ResponseMode, &g.ShadowMode, &g.ReplyCooldownSeconds,
			&g.HourlyReplyLimit, &g.AutoIssueEnabled, &g.AutoIssueConfidence,
			&g.DailyAutoIssueLimit, &g.AnswerConfidence); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetLanxinGroupPolicy 返回某个群最新的响应策略。同一个群被多个机器人加入时，
// 以最近更新的那行配置为准。
func (s *Store) GetLanxinGroupPolicy(ctx context.Context, groupID string) (*LanxinGroup, error) {
	g, err := s.scanLanxinGroupRow(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (s *Store) scanLanxinGroupRow(ctx context.Context, groupID string) (*LanxinGroup, error) {
	row := s.db.QueryRowContext(ctx, `SELECT g.id,g.bot_id,b.name,g.group_id,g.name,g.avatar_url,
        g.description,g.owner_id,g.owner_name,g.member_count,g.send_msg_status,g.active,g.synced_at,
        g.response_mode,g.shadow_mode,g.reply_cooldown_seconds,g.hourly_reply_limit,
        g.auto_issue_enabled,g.auto_issue_confidence,g.daily_auto_issue_limit,g.answer_confidence
      FROM lanxin_groups g JOIN lanxin_bots b ON b.id=g.bot_id
      WHERE g.group_id=$1 ORDER BY g.updated_at DESC LIMIT 1`, strings.TrimSpace(groupID))
	var g LanxinGroup
	err := row.Scan(&g.ID, &g.BotID, &g.BotName, &g.GroupID, &g.Name, &g.AvatarURL,
		&g.Description, &g.OwnerID, &g.OwnerName, &g.MemberCount, &g.SendMsgStatus,
		&g.Active, &g.SyncedAt, &g.ResponseMode, &g.ShadowMode, &g.ReplyCooldownSeconds,
		&g.HourlyReplyLimit, &g.AutoIssueEnabled, &g.AutoIssueConfidence,
		&g.DailyAutoIssueLimit, &g.AnswerConfidence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get lanxin group policy: %w", err)
	}
	return &g, nil
}

// UpdateLanxinGroupPolicy 更新同一个群下所有机器人的策略，避免多机器人响应不一致。
func (s *Store) UpdateLanxinGroupPolicy(ctx context.Context, g LanxinGroup) (*LanxinGroup, error) {
	mode := strings.TrimSpace(strings.ToLower(g.ResponseMode))
	switch mode {
	case "at_only", "smart", "all":
	default:
		return nil, errors.New("store: response_mode must be at_only, smart or all")
	}
	if g.ReplyCooldownSeconds < 0 || g.HourlyReplyLimit < 0 || g.DailyAutoIssueLimit < 0 {
		return nil, errors.New("store: policy limits must not be negative")
	}
	if g.AutoIssueConfidence <= 0 || g.AutoIssueConfidence > 1 {
		g.AutoIssueConfidence = 0.85
	}
	if g.AnswerConfidence <= 0 || g.AnswerConfidence > 1 {
		g.AnswerConfidence = 0.55
	}
	res, err := s.db.ExecContext(ctx, `UPDATE lanxin_groups SET
        response_mode=$2, shadow_mode=$3, reply_cooldown_seconds=$4, hourly_reply_limit=$5,
        auto_issue_enabled=$6, auto_issue_confidence=$7, daily_auto_issue_limit=$8,
        answer_confidence=$9, updated_at=now()
      WHERE group_id=$1`, strings.TrimSpace(g.GroupID), mode, g.ShadowMode, g.ReplyCooldownSeconds,
		g.HourlyReplyLimit, g.AutoIssueEnabled, g.AutoIssueConfidence, g.DailyAutoIssueLimit, g.AnswerConfidence)
	if err != nil {
		return nil, fmt.Errorf("store: update lanxin group policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetLanxinGroupPolicy(ctx, g.GroupID)
}
