package store

import (
	"context"
	"fmt"
	"sort"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 29,
		name:    "scheduler_core",
		sql: `
CREATE TABLE IF NOT EXISTS scheduler_workers (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  issue_number INTEGER NOT NULL,
  issue_url TEXT NOT NULL DEFAULT '',
  chat_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'queued',
  branch TEXT NOT NULL DEFAULT '',
  worktree TEXT NOT NULL DEFAULT '',
  pr_number INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS scheduler_workers_active_uniq
  ON scheduler_workers (project_id, issue_number)
  WHERE state NOT IN ('closed','failed');
CREATE INDEX IF NOT EXISTS scheduler_workers_state_idx ON scheduler_workers (state, created_at);

CREATE TABLE IF NOT EXISTS scheduler_links (
  platform_message_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  chat_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS scheduler_chat (
  id BIGSERIAL PRIMARY KEY,
  chat_id TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS scheduler_chat_chat_idx ON scheduler_chat (chat_id, id DESC);
`,
	},
	{
		version: 28,
		name:    "github_repo_clone_bytes",
		sql: `
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_bytes BIGINT NOT NULL DEFAULT 0;
`,
	},
	{
		version: 27,
		name:    "github_repo_clone_progress",
		sql: `
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_progress INTEGER NOT NULL DEFAULT 0;
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_phase TEXT NOT NULL DEFAULT '';
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_message TEXT NOT NULL DEFAULT '';
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_started_at TIMESTAMPTZ;
`,
	},
	{
		version: 26,
		name:    "github_repo_clone_status",
		sql: `
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_status TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 25,
		name:    "github_repo_clone",
		sql: `
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS local_path TEXT NOT NULL DEFAULT '';
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS cloned_at TIMESTAMPTZ;
ALTER TABLE github_repositories ADD COLUMN IF NOT EXISTS clone_error TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS local_repo_path TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 24,
		name:    "github_repository_catalog",
		sql: `
CREATE TABLE IF NOT EXISTS github_repositories (
    full_name       TEXT PRIMARY KEY,
    gh_id           BIGINT NOT NULL DEFAULT 0,
    name            TEXT NOT NULL DEFAULT '',
    owner           TEXT NOT NULL DEFAULT '',
    private         BOOLEAN NOT NULL DEFAULT FALSE,
    description     TEXT NOT NULL DEFAULT '',
    default_branch  TEXT NOT NULL DEFAULT '',
    language        TEXT NOT NULL DEFAULT '',
    stars           INTEGER NOT NULL DEFAULT 0,
    open_issues     INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL DEFAULT '',
    html_url        TEXT NOT NULL DEFAULT '',
    archived        BOOLEAN NOT NULL DEFAULT FALSE,
    fork            BOOLEAN NOT NULL DEFAULT FALSE,
    source          TEXT NOT NULL DEFAULT '',
    user_visible    BOOLEAN NOT NULL DEFAULT FALSE,
    synced_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS github_repositories_synced_idx ON github_repositories (synced_at DESC);
CREATE INDEX IF NOT EXISTS github_repositories_owner_idx ON github_repositories (lower(owner), full_name);
`,
	},
	{
		version: 23,
		name:    "model_profiles",
		sql: `
CREATE TABLE IF NOT EXISTS model_profiles (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    provider_id      TEXT NOT NULL REFERENCES model_providers(id) ON DELETE CASCADE,
    model_id         TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    source           TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS model_profiles_name_uniq ON model_profiles (lower(name));
CREATE INDEX IF NOT EXISTS model_profiles_provider_idx ON model_profiles (provider_id, name);
`,
	},
	{
		version: 22,
		name:    "lanxin_multi_bots",
		sql: `
CREATE TABLE IF NOT EXISTS lanxin_bots (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    credential_id       TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    app_id              TEXT NOT NULL DEFAULT '',
    api_base            TEXT NOT NULL DEFAULT '',
    require_at_me       BOOLEAN NOT NULL DEFAULT TRUE,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    status              TEXT NOT NULL DEFAULT 'disconnected',
    last_error          TEXT NOT NULL DEFAULT '',
    last_connected_at   TIMESTAMPTZ,
    last_group_sync_at  TIMESTAMPTZ,
    last_sync_error     TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS lanxin_bots_credential_uniq ON lanxin_bots (credential_id);

CREATE TABLE IF NOT EXISTS lanxin_groups (
    id               TEXT PRIMARY KEY,
    bot_id           TEXT NOT NULL REFERENCES lanxin_bots(id) ON DELETE CASCADE,
    group_id         TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    avatar_url       TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    owner_id         TEXT NOT NULL DEFAULT '',
    owner_name       TEXT NOT NULL DEFAULT '',
    member_count     INTEGER NOT NULL DEFAULT 0,
    send_msg_status  BOOLEAN NOT NULL DEFAULT TRUE,
    active           BOOLEAN NOT NULL DEFAULT TRUE,
    synced_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS lanxin_groups_bot_group_uniq ON lanxin_groups (bot_id, group_id);
CREATE INDEX IF NOT EXISTS lanxin_groups_active_idx ON lanxin_groups (active, created_at DESC);

ALTER TABLE lanxin_messages ADD COLUMN IF NOT EXISTS bot_id TEXT NOT NULL DEFAULT '';
ALTER TABLE lanxin_messages ADD COLUMN IF NOT EXISTS bot_name TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS lanxin_messages_bot_idx ON lanxin_messages (bot_id, created_at DESC);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS lanxin_bot_id TEXT NOT NULL DEFAULT '';

INSERT INTO lanxin_bots (id, name, credential_id, app_id, api_base, require_at_me, enabled, status)
SELECT 'lbot_legacy',
       COALESCE(NULLIF(metadata->>'bot_name', ''), '主机器人'),
       id,
       COALESCE(metadata->>'app_id', ''),
       COALESCE(metadata->>'api_base', ''),
       COALESCE((SELECT value <> 'false' FROM runtime_settings WHERE key = 'lanxin_require_at_me'), TRUE),
       TRUE,
       'disconnected'
  FROM credentials
 WHERE kind = 'lanxin'
 ORDER BY created_at
 LIMIT 1
ON CONFLICT DO NOTHING;

UPDATE lanxin_messages
   SET bot_id = 'lbot_legacy', bot_name = '主机器人'
 WHERE bot_id = '' AND EXISTS (SELECT 1 FROM lanxin_bots WHERE id = 'lbot_legacy');

UPDATE projects
   SET lanxin_bot_id = 'lbot_legacy'
 WHERE lanxin_bot_id = '' AND bluex_group_ids <> ''
   AND EXISTS (SELECT 1 FROM lanxin_bots WHERE id = 'lbot_legacy');
`,
	},
	{
		version: 21,
		name:    "lanxin_messages",
		sql: `
CREATE TABLE IF NOT EXISTS lanxin_messages (
    id                  BIGSERIAL PRIMARY KEY,
    platform_message_id TEXT NOT NULL UNIQUE,
    chat_type           TEXT NOT NULL DEFAULT '',
    chat_id             TEXT NOT NULL DEFAULT '',
    user_id             TEXT NOT NULL DEFAULT '',
    sender_name         TEXT NOT NULL DEFAULT '',
    group_name          TEXT NOT NULL DEFAULT '',
    msg_type            TEXT NOT NULL DEFAULT '',
    text                TEXT NOT NULL DEFAULT '',
    is_at_me            BOOLEAN NOT NULL DEFAULT false,
    project_id          TEXT,
    status              TEXT NOT NULL DEFAULT 'received',
    reason              TEXT NOT NULL DEFAULT '',
    issue_number        INTEGER NOT NULL DEFAULT 0,
    issue_url           TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS lanxin_messages_at_idx ON lanxin_messages (created_at DESC);
CREATE INDEX IF NOT EXISTS lanxin_messages_chat_idx ON lanxin_messages (chat_id, created_at DESC);
`,
	},
	{
		version: 20,
		name:    "admin_users",
		sql: `
CREATE TABLE IF NOT EXISTS admin_users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'admin',
    enabled       BOOLEAN NOT NULL DEFAULT true,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS admin_users_username_uniq ON admin_users (lower(username));

CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    user_agent TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS admin_sessions_expires_idx ON admin_sessions (expires_at);
CREATE INDEX IF NOT EXISTS admin_sessions_user_idx ON admin_sessions (user_id);
`,
	},
	{
		version: 19,
		name:    "model_providers",
		sql: `
CREATE TABLE IF NOT EXISTS model_providers (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    base_url          TEXT NOT NULL DEFAULT '',
    wire_api          TEXT NOT NULL DEFAULT 'chat',
    credential_id     TEXT NOT NULL DEFAULT '',
    enabled           BOOLEAN NOT NULL DEFAULT true,
    last_tested_at    TIMESTAMPTZ,
    last_test_ok      BOOLEAN,
    last_test_message TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS model_providers_name_uniq ON model_providers (name);

CREATE TABLE IF NOT EXISTS provider_models (
    provider_id TEXT NOT NULL,
    model_id    TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_id, model_id)
);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS codex_model_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS codex_model TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS codex_reasoning_effort TEXT NOT NULL DEFAULT '';
`,
	}, {
		version: 18,
		name:    "agent_token_expiry",
		sql: `
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS agent_tokens_expires_idx ON agent_tokens (expires_at);
`,
	},
	{
		version: 17,
		name:    "crash_similar",
		sql: `
ALTER TABLE crash_reports ADD COLUMN IF NOT EXISTS similar_issues JSONB NOT NULL DEFAULT '[]'::jsonb;
`,
	},
	{
		version: 16,
		name:    "user_identities",
		sql: `
CREATE TABLE IF NOT EXISTS user_identities (
    id               BIGSERIAL PRIMARY KEY,
    platform         TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    project_id       TEXT NOT NULL DEFAULT '',
    github_login     TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    enabled          BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS user_identities_uniq
    ON user_identities (platform, platform_user_id, project_id);
CREATE INDEX IF NOT EXISTS user_identities_lookup_idx
    ON user_identities (platform, platform_user_id);
`,
	}, {
		version: 15,
		name:    "repo_ops",
		sql: `
CREATE TABLE IF NOT EXISTS repo_ops (
    id            BIGSERIAL PRIMARY KEY,
    project_id    TEXT,
    kind          TEXT NOT NULL DEFAULT '',
    issue_ref     TEXT NOT NULL DEFAULT '',
    repo          TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL DEFAULT '',
    target_branch TEXT NOT NULL DEFAULT '',
    commit_sha    TEXT NOT NULL DEFAULT '',
    ok            BOOLEAN NOT NULL DEFAULT false,
    error         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS repo_ops_at_idx ON repo_ops (created_at DESC);
CREATE INDEX IF NOT EXISTS repo_ops_issue_idx ON repo_ops (issue_ref, created_at DESC);
`,
	}, {
		version: 14,
		name:    "tool_calls_settings",
		sql: `
CREATE TABLE IF NOT EXISTS tool_calls (
    id           BIGSERIAL PRIMARY KEY,
    project_id   TEXT,
    token_id     TEXT NOT NULL DEFAULT '',
    agent_name   TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT '',
    tool         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT '',
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    args_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tool_calls_at_idx ON tool_calls (created_at DESC);
CREATE INDEX IF NOT EXISTS tool_calls_tool_idx ON tool_calls (tool, created_at DESC);

CREATE TABLE IF NOT EXISTS runtime_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`,
	},
	{
		version: 13,
		name:    "agent_tokens",
		sql: `
CREATE TABLE IF NOT EXISTS agent_tokens (
    id          TEXT PRIMARY KEY DEFAULT ('tok_' || substr(md5(random()::text), 1, 12)),
    project_id  TEXT,
    role        TEXT NOT NULL DEFAULT 'lead',
    name        TEXT NOT NULL DEFAULT '',
    prefix      TEXT NOT NULL DEFAULT '',
    token_hash  TEXT NOT NULL UNIQUE,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    last_used_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS agent_tokens_project_idx ON agent_tokens (project_id, created_at DESC);
`,
	},
	{
		version: 12,
		name:    "crash_reports",
		sql: `
CREATE TABLE IF NOT EXISTS crash_reports (
    id            BIGSERIAL PRIMARY KEY,
    project_id    TEXT,
    fingerprint   TEXT NOT NULL DEFAULT '',
    app           TEXT NOT NULL DEFAULT '',
    version       TEXT NOT NULL DEFAULT '',
    os            TEXT NOT NULL DEFAULT '',
    arch          TEXT NOT NULL DEFAULT '',
    user_id       TEXT NOT NULL DEFAULT '',
    device_id     TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT '',
    repo          TEXT NOT NULL DEFAULT '',
    issue_number  INTEGER NOT NULL DEFAULT 0,
    issue_url     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT '',
    reject_reason TEXT NOT NULL DEFAULT '',
    occurrences   INTEGER NOT NULL DEFAULT 1,
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    bundle        JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS crash_reports_fp_idx ON crash_reports (project_id, fingerprint, created_at DESC);
CREATE INDEX IF NOT EXISTS crash_reports_user_idx ON crash_reports (user_id, created_at DESC);
`,
	},
	{
		version: 11,
		name:    "release_runs",
		sql: `
CREATE TABLE IF NOT EXISTS release_runs (
    id              BIGSERIAL PRIMARY KEY,
    project_id      TEXT,
    version         TEXT NOT NULL,
    commit_sha      TEXT NOT NULL DEFAULT '',
    branch          TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT '',
    trigger         TEXT NOT NULL DEFAULT 'manual',
    candidates      JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes           TEXT NOT NULL DEFAULT '',
    release_url     TEXT NOT NULL DEFAULT '',
    tag             TEXT NOT NULL DEFAULT '',
    release_id      BIGINT NOT NULL DEFAULT 0,
    manifest_sha256 TEXT NOT NULL DEFAULT '',
    output_dir      TEXT NOT NULL DEFAULT '',
    artifacts       JSONB NOT NULL DEFAULT '[]'::jsonb,
    build_output    TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    notify_status   TEXT NOT NULL DEFAULT '',
    notify_error    TEXT NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    duration_ms     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS release_runs_at_idx ON release_runs (started_at DESC);
CREATE INDEX IF NOT EXISTS release_runs_version_idx ON release_runs (project_id, version);
`,
	},
	{
		version: 1,
		name:    "core_schema",
		sql: `
CREATE TABLE IF NOT EXISTS projects (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',
    github_provider        TEXT NOT NULL DEFAULT 'github',
    github_repo_url        TEXT NOT NULL DEFAULT '',
    github_default_branch  TEXT NOT NULL DEFAULT 'main',
    github_issue_repo      TEXT NOT NULL DEFAULT '',
    labels_prefix          TEXT NOT NULL DEFAULT 'smartpipe',
    codeowners_required    BOOLEAN NOT NULL DEFAULT FALSE,
    approver_teams         TEXT NOT NULL DEFAULT '',
    verify_profile_path    TEXT NOT NULL DEFAULT 'smartpipe.yaml',
    release_profile        TEXT NOT NULL DEFAULT '',
    bluex_group_ids        TEXT NOT NULL DEFAULT '',
    lead_agent_template    TEXT NOT NULL DEFAULT 'pipeline-lead-v3',
    tool_profile           TEXT NOT NULL DEFAULT 'lead',
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS projects_name_key ON projects (lower(name));

CREATE TABLE IF NOT EXISTS credentials (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT REFERENCES projects(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL,
    name               TEXT NOT NULL,
    ciphertext         TEXT NOT NULL,
    mask               TEXT NOT NULL,
    metadata           JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    last_tested_at     TIMESTAMPTZ,
    last_test_ok       BOOLEAN,
    last_test_message  TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS credentials_scope_kind_name
    ON credentials (COALESCE(project_id, ''), kind, name);

CREATE INDEX IF NOT EXISTS credentials_project_idx ON credentials (project_id);

CREATE TABLE IF NOT EXISTS audit_log (
    id           BIGSERIAL PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor        TEXT NOT NULL,
    action       TEXT NOT NULL,
    target_type  TEXT NOT NULL,
    target_id    TEXT NOT NULL,
    project_id   TEXT,
    result       TEXT NOT NULL,
    detail       JSONB NOT NULL DEFAULT '{}'::jsonb,
    trace_id     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS audit_log_at_idx ON audit_log (at DESC);

CREATE TABLE IF NOT EXISTS runtime_resources (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    name          TEXT NOT NULL,
    endpoint      TEXT NOT NULL DEFAULT '',
    node_id       TEXT NOT NULL DEFAULT '',
    project_id    TEXT,
    template_name TEXT NOT NULL DEFAULT '',
    desired_state TEXT NOT NULL DEFAULT '',
    observed      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resources_kind_name
    ON runtime_resources (kind, name);
`,
	},
	{
		version: 2,
		name:    "bridge_tables",
		sql: `
CREATE TABLE IF NOT EXISTS bridge_links (
    id                BIGSERIAL PRIMARY KEY,
    source_message_id TEXT NOT NULL UNIQUE,
    channel           TEXT NOT NULL DEFAULT 'lanxin',
    chat_id           TEXT NOT NULL DEFAULT '',
    user_id           TEXT NOT NULL DEFAULT '',
    project_id        TEXT,
    repo              TEXT NOT NULL,
    issue_number      INTEGER NOT NULL,
    issue_url         TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS bridge_links_issue_idx ON bridge_links (repo, issue_number);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    delivery_id TEXT PRIMARY KEY,
    event       TEXT NOT NULL DEFAULT '',
    repo        TEXT NOT NULL DEFAULT '',
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS webhook_deliveries_at_idx ON webhook_deliveries (received_at DESC);
`,
	},
	{
		version: 10,
		name:    "releases",
		sql: `
CREATE TABLE IF NOT EXISTS releases (
    id              BIGSERIAL PRIMARY KEY,
    project_id      TEXT,
    version         TEXT NOT NULL,
    commit_sha      TEXT NOT NULL DEFAULT '',
    ok              BOOLEAN NOT NULL DEFAULT FALSE,
    status          TEXT NOT NULL DEFAULT '',
    manifest        JSONB NOT NULL DEFAULT '{}'::jsonb,
    manifest_sha256 TEXT NOT NULL DEFAULT '',
    signature       TEXT NOT NULL DEFAULT '',
    artifacts       JSONB NOT NULL DEFAULT '[]'::jsonb,
    output_dir      TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS releases_at_idx ON releases (created_at DESC);
`,
	},
	{
		version: 9,
		name:    "derivations",
		sql: `
CREATE TABLE IF NOT EXISTS derivations (
    id               BIGSERIAL PRIMARY KEY,
    project_id       TEXT,
    issue_ref        TEXT NOT NULL DEFAULT '',
    agent_name       TEXT NOT NULL DEFAULT '',
    agent_id         TEXT NOT NULL DEFAULT '',
    operation_id     TEXT NOT NULL DEFAULT '',
    approval_version INTEGER NOT NULL DEFAULT 0,
    approval_hash    TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS derivations_at_idx ON derivations (created_at DESC);
`,
	},
	{
		version: 8,
		name:    "issue_attempts",
		sql: `
CREATE TABLE IF NOT EXISTS issue_attempts (
    issue_ref            TEXT PRIMARY KEY,
    project_id           TEXT,
    attempts             INTEGER NOT NULL DEFAULT 0,
    state                TEXT NOT NULL DEFAULT 'open',
    last_verification_id BIGINT,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
`,
	},
	{
		version: 7,
		name:    "patch_verifications",
		sql: `
CREATE TABLE IF NOT EXISTS patch_verifications (
    id             BIGSERIAL PRIMARY KEY,
    project_id     TEXT,
    issue_ref      TEXT NOT NULL DEFAULT '',
    repo           TEXT NOT NULL DEFAULT '',
    base_commit    TEXT NOT NULL DEFAULT '',
    head_commit    TEXT NOT NULL DEFAULT '',
    patch_hash     TEXT NOT NULL DEFAULT '',
    ok             BOOLEAN NOT NULL DEFAULT FALSE,
    files          JSONB NOT NULL DEFAULT '[]'::jsonb,
    changed_lines  INTEGER NOT NULL DEFAULT 0,
    checks         JSONB NOT NULL DEFAULT '[]'::jsonb,
    error          TEXT NOT NULL DEFAULT '',
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    report         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS patch_verifications_at_idx ON patch_verifications (created_at DESC);
`,
	},
	{
		version: 6,
		name:    "bridge_receipt",
		sql: `
ALTER TABLE bridge_links ADD COLUMN IF NOT EXISTS receipt_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE bridge_links ADD COLUMN IF NOT EXISTS receipt_error TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 5,
		name:    "issue_plan_approvals",
		sql: `
CREATE TABLE IF NOT EXISTS issue_plans (
    id           BIGSERIAL PRIMARY KEY,
    repo         TEXT NOT NULL,
    issue_number INTEGER NOT NULL,
    version      INTEGER NOT NULL,
    body         TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    author       TEXT NOT NULL DEFAULT '',
    comment_id   BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo, issue_number, version)
);

CREATE TABLE IF NOT EXISTS issue_approvals (
    id           BIGSERIAL PRIMARY KEY,
    repo         TEXT NOT NULL,
    issue_number INTEGER NOT NULL,
    version      INTEGER NOT NULL,
    plan_hash    TEXT NOT NULL,
    approver     TEXT NOT NULL,
    comment_id   BIGINT NOT NULL DEFAULT 0,
    approved_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo, issue_number, version)
);
`,
	},
	{
		version: 4,
		name:    "binding_agent_project",
		sql: `
ALTER TABLE project_bindings ADD COLUMN IF NOT EXISTS agent_project_id TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 3,
		name:    "lead_binding_and_dispatch",
		sql: `
ALTER TABLE bridge_links ADD COLUMN IF NOT EXISTS lead_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE bridge_links ADD COLUMN IF NOT EXISTS dispatch_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE bridge_links ADD COLUMN IF NOT EXISTS dispatch_error TEXT NOT NULL DEFAULT '';
ALTER TABLE bridge_links ADD COLUMN IF NOT EXISTS dispatched_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS project_bindings (
    project_id      TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    app_server_id   TEXT NOT NULL DEFAULT '',
    app_server_addr TEXT NOT NULL DEFAULT '',
    lead_agent_id   TEXT NOT NULL DEFAULT '',
    lead_agent_name TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS bridge_dispatches (
    id          BIGSERIAL PRIMARY KEY,
    link_id     BIGINT NOT NULL REFERENCES bridge_links(id) ON DELETE CASCADE,
    agent_id    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL DEFAULT '',
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS bridge_dispatches_link_idx ON bridge_dispatches (link_id, at DESC);
`,
	},
}

// Migrate 幂等地应用所有迁移。
// migrateLockKey 是迁移用的 advisory lock，避免多个进程/测试包同时迁移互相踩。
const migrateLockKey int64 = 0x53504d50 // "SPMP"

func (s *Store) Migrate(ctx context.Context) error {
	// 同一事务连接上取锁，整个迁移期间串行；进程退出或 ctx 取消时自动释放。
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire migrate conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("store: migrate lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockKey)
	}()
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version    INT PRIMARY KEY,
        name       TEXT NOT NULL,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	for _, m := range orderedMigrations() {
		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, m.version).Scan(&exists); err != nil {
			return fmt.Errorf("store: check migration %d: %w", m.version, err)
		}
		if exists {
			continue
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

// orderedMigrations 按版本升序返回迁移，保证全新数据库也能从空库一路建到最新。
// migrations 表按“新增在前”书写便于阅读，执行顺序不能依赖声明顺序。
func orderedMigrations() []migration {
	ordered := make([]migration, len(migrations))
	copy(ordered, migrations)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].version < ordered[j].version })
	return ordered
}
