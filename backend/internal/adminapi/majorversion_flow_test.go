package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"smartpipe/backend/internal/store"
)

func TestReleaseMajorBumpRequiresConfirm(t *testing.T) {
	app, st := newTestAPI(t, "")
	rec, body := do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name": "major-gate", "github_repo_url": "https://github.com/example/major",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("project = %d: %s", rec.Code, rec.Body.String())
	}
	projectID := body["data"].(map[string]any)["id"].(string)

	// 已有一次 v1.4.0 的成功发版
	pid := projectID
	if _, err := st.CreateReleaseRun(t.Context(), store.ReleaseRun{
		ProjectID: &pid, Version: "v1.4.0", Status: "released", Trigger: "manual",
		Candidates: json.RawMessage("[]"), Artifacts: json.RawMessage("[]"), StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// v2.0.0 未确认 → 409
	rec, body = do(t, app, http.MethodPost, "/admin/releases/runs", "", map[string]any{
		"project_id": projectID, "version": "v2.0.0",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "major_confirm_required" {
		t.Fatalf("error = %v", body)
	}

	// 小版本不受影响（会继续走到 GitHub 凭据缺失而失败，但不应是 409）
	rec, _ = do(t, app, http.MethodPost, "/admin/releases/runs", "", map[string]any{
		"project_id": projectID, "version": "v1.5.0",
	})
	if rec.Code == http.StatusConflict {
		t.Fatalf("minor bump should not be blocked: %s", rec.Body.String())
	}

	// 带 confirm_major=true 的大版本请求放行（同样后续失败于凭据，不可是 409）
	rec, _ = do(t, app, http.MethodPost, "/admin/releases/runs", "", map[string]any{
		"project_id": projectID, "version": "v2.0.0", "confirm_major": true,
	})
	if rec.Code == http.StatusConflict {
		t.Fatalf("confirmed major bump should pass the gate: %s", rec.Body.String())
	}
}
