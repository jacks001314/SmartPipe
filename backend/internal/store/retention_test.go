package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCrashRetentionPurge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.DB().Exec("DELETE FROM crash_reports")

	old := time.Now().AddDate(0, 0, -40)
	bundle, _ := json.Marshal(map[string]any{"exception": map[string]any{"type": "Boom"}})
	fresh := &CrashReport{Fingerprint: "sha256:old", App: "app", Status: "created", Occurrences: 1, Bundle: bundle}
	created, err := s.CreateCrashReport(ctx, *fresh)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rejected, err := s.CreateCrashReport(ctx, CrashReport{Fingerprint: "sha256:rej", Status: "rejected", Occurrences: 0, Bundle: bundle})
	if err != nil {
		t.Fatalf("create rejected: %v", err)
	}
	keep, err := s.CreateCrashReport(ctx, CrashReport{Fingerprint: "sha256:new", Status: "created", Occurrences: 1, Bundle: bundle})
	if err != nil {
		t.Fatalf("create keep: %v", err)
	}
	// 把两条往前挪 40 天。
	if _, err := s.DB().Exec(`UPDATE crash_reports SET created_at = $1 WHERE id IN ($2,$3)`, old, created.ID, rejected.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rows, bytes, err := s.CrashStorageStats(ctx)
	if err != nil || rows != 3 || bytes <= 0 {
		t.Fatalf("stats = %d, %d, %v", rows, bytes, err)
	}

	cleared, err := s.PurgeCrashBundles(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil || cleared != 2 {
		t.Fatalf("cleared = %d, %v", cleared, err)
	}
	deleted, err := s.PurgeRejectedCrashes(ctx, time.Now().AddDate(0, 0, -7))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted = %d, %v", deleted, err)
	}

	after, err := s.GetCrashReport(ctx, created.ID)
	if err != nil {
		t.Fatalf("get purged: %v", err)
	}
	if string(after.Bundle) != "{}" {
		t.Fatalf("bundle should be emptied, got %s", after.Bundle)
	}
	if after.Fingerprint != "sha256:old" || after.Occurrences != 1 {
		t.Fatalf("row evidence lost: %+v", after)
	}
	if _, err := s.GetCrashReport(ctx, rejected.ID); err != ErrNotFound {
		t.Fatalf("rejected row should be deleted, err=%v", err)
	}
	kept, err := s.GetCrashReport(ctx, keep.ID)
	if err != nil || string(kept.Bundle) == "{}" {
		t.Fatalf("fresh bundle should stay: %v %s", err, kept.Bundle)
	}
	_, _ = s.DB().Exec("DELETE FROM crash_reports")
}
