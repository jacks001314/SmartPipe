package store

import (
	"context"
	"testing"
)

// Uses only the dedicated *_test database selected by testStore.
func TestDesktopLegacyMigrationUpgrade(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Reconstruct the old migration history while retaining desktop tables.
	// Executor hosts never existed in that branch.
	if _, err := s.db.ExecContext(ctx, `
DROP TABLE release_v2_executor_hosts;
DELETE FROM schema_migrations WHERE version BETWEEN 44 AND 52;
INSERT INTO schema_migrations(version,name) VALUES
 (44,'desktop_test_runs'),(45,'desktop_test_machines'),
 (46,'desktop_test_evidence'),(47,'desktop_gate_policy');
INSERT INTO desktop_test_machines(id,platform,token_sha256,generation)
 VALUES ('legacy-upgrade-machine','windows-amd64',repeat('a',64),7);
`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id='legacy-upgrade-machine'") })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var generation int
	if err := s.db.QueryRow("SELECT generation FROM desktop_test_machines WHERE id='legacy-upgrade-machine'").Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 7 {
		t.Fatal("legacy machine state changed")
	}
	var table string
	if err := s.db.QueryRow("SELECT to_regclass('release_v2_executor_hosts')::text").Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != "release_v2_executor_hosts" {
		t.Fatal("executor hosts missing")
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version BETWEEN 48 AND 52").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("new migration count = %d", count)
	}
}
