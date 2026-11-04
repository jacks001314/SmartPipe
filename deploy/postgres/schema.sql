-- SmartPipe Issue 存储（M1 内存实现；本 schema 供 Postgres 实现接入时使用）
CREATE TABLE IF NOT EXISTS issues (
    id               TEXT PRIMARY KEY,
    type             TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    description      TEXT        NOT NULL,
    reporter_user_id TEXT        NOT NULL,
    reporter_name    TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'new',
    group_id         TEXT,
    source_msg_id    TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS issue_comments (
    id         BIGSERIAL PRIMARY KEY,
    issue_id   TEXT        NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author     TEXT        NOT NULL,
    text       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_issues_status ON issues (status);
CREATE INDEX IF NOT EXISTS idx_issues_created ON issues (created_at DESC);
