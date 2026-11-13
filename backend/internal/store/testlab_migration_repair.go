package store

// Early desktop builds recorded desktop_test_runs as migration 44. A database
// upgraded from that build skips upstream's unrelated executor-host migration.
// Reapply the upstream DDL under a new unique version without changing history.
func init() {
	migrations = append(migrations, migration{version: 52, name: "repair_executor_hosts_after_desktop_collision", sql: `
CREATE TABLE IF NOT EXISTS release_v2_executor_hosts (
 executor_id TEXT PRIMARY KEY,
 resource_id TEXT NOT NULL DEFAULT '',
 host TEXT NOT NULL DEFAULT '',
 port INTEGER NOT NULL DEFAULT 22,
 username TEXT NOT NULL DEFAULT '',
 install_dir TEXT NOT NULL DEFAULT '',
 deployed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`})
}
