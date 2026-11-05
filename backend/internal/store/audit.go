package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// AppendAudit 写入一条审计记录。detail 会被序列化为 JSONB。
func (s *Store) AppendAudit(ctx context.Context, e AuditEntry) error {
	if e.Actor == "" {
		e.Actor = "system"
	}
	if e.Result == "" {
		e.Result = "ok"
	}
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("store: encode audit detail: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO audit_log (actor, action, target_type, target_id, project_id, result, detail, trace_id)
VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`,
		e.Actor, e.Action, e.TargetType, e.TargetID, e.ProjectID, e.Result, string(raw), e.TraceID)
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// ListAudit 返回最近的审计记录，可选按 action 前缀过滤。
func (s *Store) ListAudit(ctx context.Context, limit int, actionPrefix string) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if actionPrefix == "" {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, at, actor, action, target_type, target_id, project_id, result, detail, trace_id
  FROM audit_log ORDER BY at DESC, id DESC LIMIT $1`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, at, actor, action, target_type, target_id, project_id, result, detail, trace_id
  FROM audit_log WHERE action LIKE $2 ORDER BY at DESC, id DESC LIMIT $1`, limit, strings.ReplaceAll(actionPrefix, "*", "%")+"%")
	}
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var (
			e       AuditEntry
			project sql.NullString
			raw     []byte
		)
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.TargetType, &e.TargetID,
			&project, &e.Result, &raw, &e.TraceID); err != nil {
			return nil, err
		}
		if project.Valid {
			v := project.String
			e.ProjectID = &v
		}
		e.Detail = map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
