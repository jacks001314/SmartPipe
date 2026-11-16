package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

const releaseV2RunColumns = `id, plan_id, project_id, repo, requested_by, trigger, mode, channel, status,
version, build_version, tag, source_branch, source_commit, resolved_commit, profile_sha256, plan_sha256,
source_sha256, source_storage_key, manifest_storage_key, executor_ids, update_servers, recovery_attempts,
idempotency_key, agent_thread_id, notes_md, notes_sha256, approval_state, error_code, error_message,
clone_credential, started_at, finished_at, created_at, updated_at`

func (s *Store) CreateReleaseV2Plan(ctx context.Context, plan *releasev2.Plan) error {
	if plan == nil {
		return fmt.Errorf("%w: plan is nil", releasev2.ErrInvalid)
	}
	candidates, _ := json.Marshal(plan.Candidates)
	targets, _ := json.Marshal(plan.Targets)
	blockers, _ := json.Marshal(plan.Blockers)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO release_v2_plans (
  id, project_id, requested_by, trigger, mode, status, version, build_version, tag, channel, repo,
  source_branch, source_commit, resolved_commit, profile_sha256, plan_sha256, candidates, targets,
  blockers, notes_md, notes_sha256, clone_credential, expires_at, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
		plan.ID, plan.ProjectID, plan.RequestedBy, plan.Trigger, plan.Mode, plan.Status, plan.Version,
		plan.BuildVersion, plan.Tag, plan.Channel, plan.Repo, plan.SourceBranch, plan.SourceCommit,
		plan.ResolvedCommit, plan.ProfileSHA256, plan.PlanSHA256, candidates, targets, blockers,
		plan.NotesMD, plan.NotesSHA256, plan.CloneCredential, plan.ExpiresAt, plan.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: release plan already exists", ErrConflict)
		}
		return fmt.Errorf("store: create release v2 plan: %w", err)
	}
	return nil
}

func (s *Store) GetReleaseV2Plan(ctx context.Context, id string) (*releasev2.Plan, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, requested_by, trigger, mode, status, version, build_version, tag, channel, repo,
       source_branch, source_commit, resolved_commit, profile_sha256, plan_sha256, candidates, targets,
       blockers, notes_md, notes_sha256, clone_credential, expires_at, created_at
  FROM release_v2_plans WHERE id=$1`, id)
	p, err := scanReleaseV2Plan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) CreateRunFromPlan(ctx context.Context, run *releasev2.Run, targets []releasev2.Target) (*releasev2.Run, error) {
	if run == nil {
		return nil, fmt.Errorf("%w: run is nil", releasev2.ErrInvalid)
	}
	if strings.TrimSpace(run.IdempotencyKey) != "" {
		if existing, err := s.releaseV2RunByIDempotency(ctx, run.ProjectID, run.IdempotencyKey); err == nil {
			return existing, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin create release v2 run: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
UPDATE release_v2_plans SET status='consumed'
 WHERE id=$1 AND status='planned' AND (expires_at IS NULL OR expires_at > now())`, run.PlanID)
	if err != nil {
		return nil, fmt.Errorf("store: consume release v2 plan: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("%w: plan is not runnable", ErrConflict)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO release_v2_runs (
  id, plan_id, project_id, repo, requested_by, trigger, mode, channel, status, version, build_version,
  tag, source_branch, source_commit, resolved_commit, profile_sha256, plan_sha256, idempotency_key,
  agent_thread_id, notes_md, notes_sha256, approval_state, error_code, error_message, clone_credential,
  created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
		run.ID, run.PlanID, run.ProjectID, run.Repo, run.RequestedBy, run.Trigger, run.Mode, run.Channel,
		run.Status, run.Version, run.BuildVersion, run.Tag, run.SourceBranch, run.SourceCommit,
		run.ResolvedCommit, run.ProfileSHA256, run.PlanSHA256, run.IdempotencyKey, run.AgentThreadID,
		run.NotesMD, run.NotesSHA256, run.ApprovalState, run.ErrorCode, run.ErrorMessage, run.CloneCredential,
		run.CreatedAt, run.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback()
			if strings.TrimSpace(run.IdempotencyKey) != "" {
				if existing, getErr := s.releaseV2RunByIDempotency(ctx, run.ProjectID, run.IdempotencyKey); getErr == nil {
					return existing, nil
				}
			}
			return nil, fmt.Errorf("%w: an active release run already exists for this project/channel", ErrConflict)
		}
		return nil, fmt.Errorf("store: insert release v2 run: %w", err)
	}
	for _, target := range targets {
		_, err = tx.ExecContext(ctx, `
INSERT INTO release_v2_targets (
  run_id, target_id, os, arch, executor_id, required, status, attempt, build_command, sign_command,
  verify_command, artifact_rules, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			run.ID, target.TargetID, target.OS, target.Arch, target.ExecutorID, target.Required,
			target.Status, target.Attempt, target.BuildCommand, target.SignCommand, target.VerifyCommand, mustJSON(target.Artifacts),
			target.CreatedAt, target.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("store: insert release v2 target %s: %w", target.TargetID, err)
		}
	}
	if err := appendReleaseV2EventTx(ctx, tx, releasev2.Event{
		RunID: run.ID, Actor: firstStoreNonEmpty(run.RequestedBy, "release-service"), Kind: "run.created",
		Summary: "release run created", Detail: map[string]any{"plan_id": run.PlanID, "version": run.Version, "mode": run.Mode},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit release v2 run: %w", err)
	}
	return run, nil
}

func (s *Store) releaseV2RunByIDempotency(ctx context.Context, projectID, key string) (*releasev2.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseV2RunColumns+`
  FROM release_v2_runs WHERE project_id=$1 AND idempotency_key=$2`, projectID, key)
	run, err := scanReleaseV2Run(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return run, err
}

func (s *Store) GetRun(ctx context.Context, id string) (*releasev2.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseV2RunColumns+` FROM release_v2_runs WHERE id=$1`, id)
	run, err := scanReleaseV2Run(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return run, err
}

// SetRunSource 记录一次运行的冻结源码归档，执行器之后只从这里取源码。
func (s *Store) SetRunSource(ctx context.Context, runID, sha256, storageKey string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET source_sha256=$2, source_storage_key=$3, updated_at=now() WHERE id=$1`,
		runID, sha256, storageKey)
	if err != nil {
		return fmt.Errorf("store: set run source: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTargetManifest 记录某个 target 已上传的制品清单对象。
func (s *Store) SetTargetManifest(ctx context.Context, runID, targetID, storageKey string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET manifest_path=$3, updated_at=now() WHERE run_id=$1 AND target_id=$2`,
		runID, targetID, storageKey)
	if err != nil {
		return fmt.Errorf("store: set target manifest: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRunManifest 记录一次运行合并后的完整制品清单对象。
func (s *Store) SetRunManifest(ctx context.Context, runID, storageKey string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET manifest_storage_key=$2, updated_at=now() WHERE id=$1`, runID, storageKey)
	if err != nil {
		return fmt.Errorf("store: set run manifest: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListRuns(ctx context.Context, projectID string, limit int) ([]releasev2.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + releaseV2RunColumns + ` FROM release_v2_runs`
	args := []any{}
	if strings.TrimSpace(projectID) != "" {
		query += ` WHERE project_id=$1`
		args = append(args, strings.TrimSpace(projectID))
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list release v2 runs: %w", err)
	}
	defer rows.Close()
	out := make([]releasev2.Run, 0, limit)
	for rows.Next() {
		run, err := scanReleaseV2Run(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func (s *Store) ListTargets(ctx context.Context, runID string) ([]releasev2.Target, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, target_id, os, arch, executor_id, required, status, attempt, lease_owner,
       lease_expires_at, build_command, sign_command, verify_command, artifact_rules, error_code, error_message,
       manifest_path, started_at, finished_at, created_at, updated_at
  FROM release_v2_targets WHERE run_id=$1 ORDER BY target_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list release v2 targets: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Target{}
	for rows.Next() {
		var t releasev2.Target
		var lease, started, finished sql.NullTime
		var rules []byte
		if err := rows.Scan(&t.ID, &t.RunID, &t.TargetID, &t.OS, &t.Arch, &t.ExecutorID, &t.Required,
			&t.Status, &t.Attempt, &t.LeaseOwner, &lease, &t.BuildCommand, &t.SignCommand, &t.VerifyCommand, &rules,
			&t.ErrorCode, &t.ErrorMessage, &t.ManifestPath, &started, &finished, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(rules, &t.Artifacts)
		t.LeaseExpiresAt = nullTimePtr(lease)
		t.StartedAt = nullTimePtr(started)
		t.FinishedAt = nullTimePtr(finished)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ListArtifacts(ctx context.Context, runID string) ([]releasev2.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, target_id, attempt, role, name, content_type, size, sha256, storage_key,
       kind, path, os, arch, destinations, platform_signed, manifest_signed, created_at
  FROM release_v2_artifacts WHERE run_id=$1 ORDER BY target_id, role, name`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list release v2 artifacts: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Artifact{}
	for rows.Next() {
		var a releasev2.Artifact
		var destinations []byte
		if err := rows.Scan(&a.ID, &a.RunID, &a.TargetID, &a.Attempt, &a.Role, &a.Name, &a.ContentType,
			&a.Size, &a.SHA256, &a.StorageKey, &a.Kind, &a.Path, &a.OS, &a.Arch, &destinations,
			&a.PlatformSigned, &a.ManifestSigned, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(destinations, &a.Destinations)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AddArtifact(ctx context.Context, a releasev2.Artifact) (*releasev2.Artifact, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_artifacts (
  run_id, target_id, attempt, role, kind, path, os, arch, destinations, name, content_type, size, sha256, storage_key,
  platform_signed, manifest_signed
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT (run_id, target_id, attempt, role, name) DO UPDATE SET
  kind=EXCLUDED.kind, path=EXCLUDED.path, os=EXCLUDED.os, arch=EXCLUDED.arch,
  destinations=EXCLUDED.destinations, content_type=EXCLUDED.content_type, size=EXCLUDED.size, sha256=EXCLUDED.sha256,
  storage_key=EXCLUDED.storage_key, platform_signed=EXCLUDED.platform_signed,
  manifest_signed=EXCLUDED.manifest_signed
WHERE release_v2_artifacts.sha256=EXCLUDED.sha256
RETURNING id, created_at`, a.RunID, a.TargetID, a.Attempt, a.Role, a.Kind, a.Path, a.OS, a.Arch,
		mustJSON(a.Destinations), a.Name, a.ContentType, a.Size, a.SHA256, a.StorageKey,
		a.PlatformSigned, a.ManifestSigned)
	if err := row.Scan(&a.ID, &a.CreatedAt); err != nil {
		if err == sql.ErrNoRows || isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: artifact already exists with a different digest", ErrConflict)
		}
		return nil, fmt.Errorf("store: add release v2 artifact: %w", err)
	}
	return &a, nil
}

// ReplaceRunArtifact upserts a run-level artifact (target_id = ”) even when
// its digest changed. Rebuilding the canonical upgrade bundle legitimately
// produces new bytes, and AddArtifact would reject that as a conflict.
func (s *Store) ReplaceRunArtifact(ctx context.Context, a releasev2.Artifact) (*releasev2.Artifact, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_artifacts (
  run_id, target_id, attempt, role, kind, path, os, arch, destinations, name, content_type, size, sha256, storage_key,
  platform_signed, manifest_signed
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT (run_id, target_id, attempt, role, name) DO UPDATE SET
  kind=EXCLUDED.kind, path=EXCLUDED.path, os=EXCLUDED.os, arch=EXCLUDED.arch,
  destinations=EXCLUDED.destinations, content_type=EXCLUDED.content_type, size=EXCLUDED.size, sha256=EXCLUDED.sha256,
  storage_key=EXCLUDED.storage_key, platform_signed=EXCLUDED.platform_signed,
  manifest_signed=EXCLUDED.manifest_signed
RETURNING id, created_at`, a.RunID, a.TargetID, a.Attempt, a.Role, a.Kind, a.Path, a.OS, a.Arch,
		mustJSON(a.Destinations), a.Name, a.ContentType, a.Size, a.SHA256, a.StorageKey,
		a.PlatformSigned, a.ManifestSigned)
	if err := row.Scan(&a.ID, &a.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: replace release v2 artifact: %w", err)
	}
	return &a, nil
}

func (s *Store) UpdateRunStatus(ctx context.Context, id string, from, to releasev2.RunStatus, code, message string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_runs SET
  status=$3,
  error_code=CASE WHEN $4 <> '' THEN $4
                  WHEN $3 IN ('queued','preparing','building','verifying','artifacts_ready','recovering','staged','publishing') THEN ''
                  ELSE error_code END,
  error_message=CASE WHEN $5 <> '' THEN $5
                     WHEN $3 IN ('queued','preparing','building','verifying','artifacts_ready','recovering','staged','publishing') THEN ''
                     ELSE error_message END,
  started_at=COALESCE(started_at, CASE WHEN $3 IN ('preparing','building','verifying') THEN now() ELSE started_at END),
  finished_at=CASE WHEN $3 IN ('published','built','failed','cancelled') THEN now()
                   WHEN $3 IN ('queued','preparing','building','verifying','artifacts_ready','recovering','needs_attention','staged','publishing') THEN NULL
                   ELSE finished_at END,
  -- 只有真正收口才清零。needs_attention 与 artifacts_ready 之间的来回
  -- （runner 按事实状态收口、Agent 会话异常退出）属于同一轮恢复，清零会让
  -- 重试上限失效并可能无限循环。
  recovery_attempts=CASE WHEN $3 IN ('published','built','cancelled') THEN 0 ELSE recovery_attempts END,
  updated_at=now()
WHERE id=$1 AND status=$2`, id, from, to, code, message)
	if err != nil {
		return false, fmt.Errorf("store: update release v2 run status: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) AppendEvent(ctx context.Context, e releasev2.Event) error {
	return appendReleaseV2EventTx(ctx, s.db, e)
}

type releaseV2Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendReleaseV2EventTx(ctx context.Context, ex releaseV2Execer, e releasev2.Event) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, _ := json.Marshal(detail)
	// jsonb 不接受 \u0000 转义；命令输出里混入的 NUL 不该让整条事件丢失。
	raw = bytes.ReplaceAll(raw, []byte(`\u0000`), []byte(`\uFFFD`))
	if _, err := ex.ExecContext(ctx, `
INSERT INTO release_v2_events (run_id, actor, kind, target_id, summary, detail)
VALUES ($1,$2,$3,$4,$5,$6)`, e.RunID, e.Actor, e.Kind, e.TargetID, sanitizeText(e.Summary), raw); err != nil {
		return fmt.Errorf("store: append release v2 event: %w", err)
	}
	return nil
}

func (s *Store) ListEvents(ctx context.Context, runID string, after int64, limit int) ([]releasev2.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, actor, kind, target_id, summary, detail, created_at
  FROM release_v2_events WHERE run_id=$1 AND id>$2 ORDER BY id LIMIT $3`, runID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list release v2 events: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Event{}
	for rows.Next() {
		var e releasev2.Event
		var detail []byte
		if err := rows.Scan(&e.ID, &e.RunID, &e.Actor, &e.Kind, &e.TargetID, &e.Summary, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detail, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// rowScanner is the subset of *sql.Row/*sql.Rows used by scan helpers.
type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) ListExecutors(ctx context.Context) ([]releasev2.Executor, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, os, arch, labels, capabilities, version, status, last_heartbeat_at, last_error, created_at, updated_at
  FROM release_v2_executors ORDER BY os, arch, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list release executors: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Executor{}
	for rows.Next() {
		var x releasev2.Executor
		var labels, caps []byte
		var heartbeat sql.NullTime
		if err := rows.Scan(&x.ID, &x.OS, &x.Arch, &labels, &caps, &x.Version, &x.Status,
			&heartbeat, &x.LastError, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(labels, &x.Labels)
		_ = json.Unmarshal(caps, &x.Capabilities)
		x.LastHeartbeatAt = nullTimePtr(heartbeat)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) UpsertExecutor(ctx context.Context, x releasev2.Executor) error {
	labels, _ := json.Marshal(x.Labels)
	caps, _ := json.Marshal(x.Capabilities)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO release_v2_executors (id, os, arch, labels, capabilities, version, status, last_heartbeat_at, last_error)
VALUES ($1,$2,$3,$4,$5,$6,$7,now(),$8)
ON CONFLICT (id) DO UPDATE SET
  os=EXCLUDED.os, arch=EXCLUDED.arch, labels=EXCLUDED.labels, capabilities=EXCLUDED.capabilities,
  version=EXCLUDED.version, status=EXCLUDED.status, last_heartbeat_at=now(),
  last_error=EXCLUDED.last_error, updated_at=now()`,
		x.ID, x.OS, x.Arch, labels, caps, x.Version, x.Status, x.LastError)
	if err != nil {
		return fmt.Errorf("store: upsert release executor: %w", err)
	}
	return nil
}

func (s *Store) CreateReleaseV2Token(ctx context.Context, token releasev2.Token) error {
	scopes, _ := json.Marshal(token.Scopes)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO release_v2_tokens (id, run_id, project_id, token_hash, scopes, expires_at)
VALUES ($1,$2,$3,$4,$5,$6)`, token.ID, token.RunID, token.ProjectID, token.TokenHash, scopes, token.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create release v2 token: %w", err)
	}
	return nil
}

// RevokeReleaseV2Token 立即失效一个 token（发布 Agent 会话结束时调用）。
func (s *Store) RevokeReleaseV2Token(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE release_v2_tokens SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id); err != nil {
		return fmt.Errorf("store: revoke release v2 token: %w", err)
	}
	return nil
}

func (s *Store) AuthenticateReleaseV2Token(ctx context.Context, tokenHash string) (*releasev2.Token, error) {
	row := s.db.QueryRowContext(ctx, `
UPDATE release_v2_tokens SET last_used_at=now()
 WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now()
 RETURNING id, run_id, project_id, token_hash, scopes, expires_at, revoked_at, last_used_at, created_at`, tokenHash)
	var t releasev2.Token
	var scopes []byte
	var revoked, used sql.NullTime
	if err := row.Scan(&t.ID, &t.RunID, &t.ProjectID, &t.TokenHash, &scopes, &t.ExpiresAt, &revoked, &used, &t.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: authenticate release v2 token: %w", err)
	}
	_ = json.Unmarshal(scopes, &t.Scopes)
	t.RevokedAt = nullTimePtr(revoked)
	t.LastUsedAt = nullTimePtr(used)
	return &t, nil
}

func (s *Store) UpsertPublication(ctx context.Context, p releasev2.Publication) (*releasev2.Publication, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO release_v2_publications (
  run_id, provider, target_id, state, external_id, external_url, request_sha256, error_message
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (run_id, provider, target_id) DO UPDATE SET
  state=EXCLUDED.state, external_id=EXCLUDED.external_id, external_url=EXCLUDED.external_url,
  request_sha256=EXCLUDED.request_sha256, error_message=EXCLUDED.error_message, updated_at=now()
RETURNING id, created_at, updated_at`, p.RunID, p.Provider, p.TargetID, p.State, p.ExternalID,
		p.ExternalURL, p.RequestSHA256, p.ErrorMessage)
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store: upsert release publication: %w", err)
	}
	return &p, nil
}

func (s *Store) ListPublications(ctx context.Context, runID string) ([]releasev2.Publication, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, provider, target_id, state, external_id, external_url, request_sha256,
       error_message, created_at, updated_at
  FROM release_v2_publications WHERE run_id=$1 ORDER BY provider, target_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list release publications: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Publication{}
	for rows.Next() {
		var p releasev2.Publication
		if err := rows.Scan(&p.ID, &p.RunID, &p.Provider, &p.TargetID, &p.State, &p.ExternalID,
			&p.ExternalURL, &p.RequestSHA256, &p.ErrorMessage, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanReleaseV2Plan(row rowScanner) (*releasev2.Plan, error) {
	var p releasev2.Plan
	var candidates, targets, blockers []byte
	var expires sql.NullTime
	if err := row.Scan(&p.ID, &p.ProjectID, &p.RequestedBy, &p.Trigger, &p.Mode, &p.Status,
		&p.Version, &p.BuildVersion, &p.Tag, &p.Channel, &p.Repo, &p.SourceBranch, &p.SourceCommit,
		&p.ResolvedCommit, &p.ProfileSHA256, &p.PlanSHA256, &candidates, &targets, &blockers,
		&p.NotesMD, &p.NotesSHA256, &p.CloneCredential, &expires, &p.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(candidates, &p.Candidates)
	_ = json.Unmarshal(targets, &p.Targets)
	_ = json.Unmarshal(blockers, &p.Blockers)
	if expires.Valid {
		p.ExpiresAt = expires.Time
	}
	return &p, nil
}

func scanReleaseV2Run(row rowScanner) (*releasev2.Run, error) {
	var r releasev2.Run
	var started, finished sql.NullTime
	var executorIDs, updateServers []byte
	if err := row.Scan(&r.ID, &r.PlanID, &r.ProjectID, &r.Repo, &r.RequestedBy, &r.Trigger, &r.Mode,
		&r.Channel, &r.Status, &r.Version, &r.BuildVersion, &r.Tag, &r.SourceBranch, &r.SourceCommit,
		&r.ResolvedCommit, &r.ProfileSHA256, &r.PlanSHA256, &r.SourceSHA256, &r.SourceStorageKey,
		&r.ManifestStorageKey, &executorIDs, &updateServers, &r.RecoveryAttempts,
		&r.IdempotencyKey, &r.AgentThreadID,
		&r.NotesMD, &r.NotesSHA256, &r.ApprovalState, &r.ErrorCode, &r.ErrorMessage, &r.CloneCredential,
		&started, &finished, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(executorIDs, &r.ExecutorIDs)
	_ = json.Unmarshal(updateServers, &r.UpdateServers)
	r.StartedAt = nullTimePtr(started)
	r.FinishedAt = nullTimePtr(finished)
	return &r, nil
}

func nullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func firstStoreNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ReleaseV2RunRetentions 批量返回清理器需要的运行信息。
func (s *Store) ReleaseV2RunRetentions(ctx context.Context, ids []string) (map[string]releasev2.RunRetention, error) {
	out := map[string]releasev2.RunRetention{}
	clean := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(clean))
	args := make([]any, len(clean))
	for i, id := range clean {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, project_id, channel, finished_at, updated_at FROM release_v2_runs WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: release v2 run retentions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var info releasev2.RunRetention
		var finished sql.NullTime
		if err := rows.Scan(&id, &info.Status, &info.ProjectID, &info.Channel, &finished, &info.UpdatedAt); err != nil {
			return nil, err
		}
		info.FinishedAt = nullTimePtr(finished)
		out[id] = info
	}
	return out, rows.Err()
}

func (s *Store) ListActiveReleaseV2Runs(ctx context.Context, limit int) ([]releasev2.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseV2RunColumns+`
  FROM release_v2_runs
 WHERE status IN ('queued','preparing','building','verifying','artifacts_ready','recovering','needs_attention','staged','publishing')
 ORDER BY created_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list active release v2 runs: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Run{}
	for rows.Next() {
		run, err := scanReleaseV2Run(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

// ListAttentionReleaseV2Runs 返回需要交回发布 Agent 的运行。
//
// 它比 ListActiveReleaseV2Runs 多一个 partially_published：渠道指针已经推进、
// GitHub 还没收口，会话一旦中断就没人继续，必须能被自动接管。单独开一条查询，
// 是为了不让长期滞留的半发布运行挤占 runner 的活跃运行额度（runner 只看
// 前 N 条，老运行排在前面会把新运行饿死）。
func (s *Store) ListAttentionReleaseV2Runs(ctx context.Context, limit int) ([]releasev2.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseV2RunColumns+`
  FROM release_v2_runs
 WHERE status IN ('queued','preparing','building','verifying','artifacts_ready','recovering','needs_attention','staged','publishing','partially_published')
 ORDER BY created_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list attention release v2 runs: %w", err)
	}
	defer rows.Close()
	out := []releasev2.Run{}
	for rows.Next() {
		run, err := scanReleaseV2Run(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

// BumpReleaseV2RecoveryAttempts records one automatic Agent takeover for a run
// that is waiting for attention. It returns 0 when the run already left the
// attention-managed states or reached max (max <= 0 means unlimited).
// partially_published 也算待接管：渠道指针已经推进、GitHub 还没收口，会话一旦
// 中断就没人继续，必须和 needs_attention 共用同一份重试预算，才不会无限接管。
// artifacts_ready / recovering / staged / publishing 同理：publish 模式的接管
// 起点经常就是这几个中间态（构建刚完成、灌库刚失败），排除在外会让重试永远
// 记不上数，最终变成无限空转。
func (s *Store) BumpReleaseV2RecoveryAttempts(ctx context.Context, runID string, max int) (int, error) {
	var attempts int
	err := s.db.QueryRowContext(ctx, `
UPDATE release_v2_runs SET recovery_attempts=recovery_attempts+1, updated_at=now()
WHERE id=$1
  AND status IN ('needs_attention','partially_published','artifacts_ready','recovering','staged','publishing')
  AND ($2 <= 0 OR recovery_attempts < $2)
RETURNING recovery_attempts`, runID, max).Scan(&attempts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: bump release v2 recovery attempts: %w", err)
	}
	return attempts, nil
}

func (s *Store) ClaimReleaseV2Target(ctx context.Context, executorID, osName, arch string, leaseTTL time.Duration) (*releasev2.Target, error) {
	if leaseTTL <= 0 {
		leaseTTL = 2 * time.Minute
	}
	row := s.db.QueryRowContext(ctx, `
WITH candidate AS (
  SELECT t.id
    FROM release_v2_targets t
    JOIN release_v2_runs r ON r.id=t.run_id
   WHERE (t.executor_id=$1 OR (t.executor_id='' AND t.os=$2 AND t.arch=$3))
     AND t.status IN ('queued','retry_queued')
     AND r.status='building'
   ORDER BY t.id
   FOR UPDATE OF t SKIP LOCKED
   LIMIT 1
)
UPDATE release_v2_targets t SET
  status='leased', lease_owner=$1,
  lease_expires_at=now()+make_interval(secs => $4::double precision),
  started_at=COALESCE(started_at,now()), updated_at=now()
FROM candidate
WHERE t.id=candidate.id
RETURNING t.id, t.run_id, t.target_id, t.os, t.arch, t.executor_id, t.required, t.status,
          t.attempt, t.lease_owner, t.lease_expires_at, t.build_command, t.sign_command, t.verify_command, t.artifact_rules,
          t.error_code, t.error_message, t.manifest_path, t.started_at, t.finished_at, t.created_at, t.updated_at`,
		executorID, strings.ToLower(osName), strings.ToLower(arch), leaseTTL.Seconds())
	var target releasev2.Target
	var lease, started, finished sql.NullTime
	var rules []byte
	if err := row.Scan(&target.ID, &target.RunID, &target.TargetID, &target.OS, &target.Arch,
		&target.ExecutorID, &target.Required, &target.Status, &target.Attempt, &target.LeaseOwner,
		&lease, &target.BuildCommand, &target.SignCommand, &target.VerifyCommand, &rules, &target.ErrorCode, &target.ErrorMessage,
		&target.ManifestPath, &started, &finished, &target.CreatedAt, &target.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) || isUniqueViolation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: claim release v2 target: %w", err)
	}
	_ = json.Unmarshal(rules, &target.Artifacts)
	target.LeaseExpiresAt = nullTimePtr(lease)
	target.StartedAt = nullTimePtr(started)
	target.FinishedAt = nullTimePtr(finished)
	return &target, nil
}

func (s *Store) HeartbeatReleaseV2Target(ctx context.Context, runID, targetID, executorID string, leaseTTL time.Duration) (bool, error) {
	if leaseTTL <= 0 {
		leaseTTL = 2 * time.Minute
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET
  status=CASE WHEN status='leased' THEN 'building' ELSE status END,
  lease_expires_at=now()+make_interval(secs => $4::double precision), updated_at=now()
WHERE run_id=$1 AND target_id=$2 AND lease_owner=$3
  AND status IN ('leased','building','verifying','uploaded')
  AND EXISTS (
    SELECT 1 FROM release_v2_runs r
    WHERE r.id=$1 AND r.status IN (
      'queued','preparing','building','verifying','artifacts_ready','staged','publishing','needs_attention','recovering'
    )
  )`, runID, targetID, executorID, leaseTTL.Seconds())
	if err != nil {
		return false, fmt.Errorf("store: heartbeat release v2 target: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) FinishReleaseV2Target(ctx context.Context, runID, targetID, executorID string, ok bool, code, message string) (bool, error) {
	status := releasev2.TargetFailed
	if ok {
		status = releasev2.TargetReady
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET
  status=$4, error_code=$5, error_message=$6, finished_at=now(), updated_at=now(),
  lease_expires_at=NULL
WHERE run_id=$1 AND target_id=$2 AND lease_owner=$3
  AND status IN ('leased','building','verifying','uploaded')`, runID, targetID, executorID, status, code, message)
	if err != nil {
		return false, fmt.Errorf("store: finish release v2 target: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RequeueReleaseV2TargetLease 把仍由执行器持租的目标退回 retry_queued。
// 对象存储短暂不可用这类瞬时故障走这里：目标重排后由执行器再试一次，
// attempt 达到上限才返回 false，由调用方按真正失败处理。
func (s *Store) RequeueReleaseV2TargetLease(ctx context.Context, runID, targetID, executorID, code, message string, maxAttempt int) (bool, error) {
	if maxAttempt <= 0 {
		maxAttempt = 3
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET
  status='retry_queued', attempt=attempt+1, lease_owner='', lease_expires_at=NULL,
  error_code=$4, error_message=$5, started_at=NULL, finished_at=NULL, updated_at=now()
WHERE run_id=$1 AND target_id=$2 AND lease_owner=$3
  AND status IN ('leased','building','verifying','uploaded') AND attempt < $6`,
		runID, targetID, executorID, code, message, maxAttempt)
	if err != nil {
		return false, fmt.Errorf("store: requeue release v2 target: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) RetryReleaseV2Target(ctx context.Context, runID, targetID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE release_v2_targets SET status='retry_queued', attempt=attempt+1,
  error_code='', error_message='', lease_owner='', lease_expires_at=NULL,
  started_at=NULL, finished_at=NULL, updated_at=now()
-- uploaded 表示"制品传了一半、清单没交"（例如取消导致的构建中止）。这种 target
-- 不是终态，必须允许重试，否则整条 run 会永远停在 needs_attention。
WHERE run_id=$1 AND target_id=$2 AND status IN ('failed','uploaded')`, runID, targetID)
	if err != nil {
		return false, fmt.Errorf("store: retry release v2 target: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return raw
}
