package store

import (
	"context"
	"fmt"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
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
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version    INT PRIMARY KEY,
        name       TEXT NOT NULL,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	for _, m := range migrations {
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
