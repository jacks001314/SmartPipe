package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCrashSimilarRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.DB().Exec("DELETE FROM crash_reports WHERE user_id = 'test-similar-row'")

	raw, _ := json.Marshal([]map[string]any{{"issue_number": 42, "score": 0.8}})
	created, err := s.CreateCrashReport(ctx, CrashReport{
		Fingerprint: "sha256:sim", App: "app", Status: "created", Occurrences: 1,
		UserID: "test-similar-row", Bundle: json.RawMessage(`{"app":"app"}`), Similar: raw,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetCrashReport(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Similar) == 0 || string(got.Similar) == "[]" {
		t.Fatalf("similar not persisted: %s", got.Similar)
	}
	var items []map[string]any
	if err := json.Unmarshal(got.Similar, &items); err != nil || len(items) != 1 || items[0]["issue_number"].(float64) != 42 {
		t.Fatalf("similar payload = %s (%v)", got.Similar, err)
	}

	// 回填覆盖
	if err := s.SetCrashSimilar(ctx, created.ID, json.RawMessage(`[{"issue_number":7,"score":0.6}]`)); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ = s.GetCrashReport(ctx, created.ID)
	if string(got.Similar) == "" || string(got.Similar) == string(raw) {
		t.Fatalf("similar not updated: %s", got.Similar)
	}

	// 未提供时默认空数组
	plain, err := s.CreateCrashReport(ctx, CrashReport{Fingerprint: "sha256:plain", App: "app", Status: "created", UserID: "test-similar-row", Bundle: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if string(plain.Similar) != "[]" {
		t.Fatalf("default similar = %s", plain.Similar)
	}
	_, _ = s.DB().Exec("DELETE FROM crash_reports WHERE user_id = 'test-similar-row'")
}
