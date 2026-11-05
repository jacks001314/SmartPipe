package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/conncheck"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

func newTestAPI(t *testing.T, token string) (*iris.Application, *store.Store) {
	return newTestAPIWith(t, token, nil)
}

func newTestAPIWith(t *testing.T, token string, mutate func(*Config)) (*iris.Application, *store.Store) {
	t.Helper()
	dsn := os.Getenv("SMARTPIPE_TEST_DSN")
	if dsn == "" {
		t.Skip("SMARTPIPE_TEST_DSN 未设置，跳过管理 API 集成测试")
	}
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(3*i + 1)
	}
	box, _ := secretbox.New(key)
	ctx := context.Background()
	st, err := store.Open(ctx, dsn, box)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = st.DB().Exec("DELETE FROM credentials")
	_, _ = st.DB().Exec("DELETE FROM audit_log")
	_, _ = st.DB().Exec("DELETE FROM projects")
	_, _ = st.DB().Exec("DELETE FROM repo_ops")
	t.Cleanup(func() {
		_, _ = st.DB().Exec("DELETE FROM credentials")
		_, _ = st.DB().Exec("DELETE FROM audit_log")
		_, _ = st.DB().Exec("DELETE FROM projects")
		_, _ = st.DB().Exec("DELETE FROM repo_ops")
		_ = st.Close()
	})

	cfg := Config{Store: st, Checker: conncheck.New(), AdminToken: token, Version: "test"}
	if mutate != nil {
		mutate(&cfg)
	}
	api := New(cfg)
	app := iris.New()
	api.Register(app)
	if err := app.Build(); err != nil {
		t.Fatalf("build iris: %v", err)
	}
	return app, st
}

func do(t *testing.T, app http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestHealthReportsDatabase(t *testing.T) {
	app, _ := newTestAPI(t, "")
	rec, body := do(t, app, http.MethodGet, "/admin/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	data, _ := body["data"].(map[string]any)
	if data["database"] != true {
		t.Fatalf("database should be true: %v", data)
	}
}

func TestProjectAndCredentialFlow(t *testing.T) {
	app, _ := newTestAPI(t, "")

	rec, body := do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name":            "API Project",
		"github_repo_url": "https://github.com/example/api",
		"bluex_group_ids": "grp-9",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d: %s", rec.Code, rec.Body.String())
	}
	project := body["data"].(map[string]any)
	projectID := project["id"].(string)
	if projectID == "" {
		t.Fatal("missing project id")
	}

	rec, body = do(t, app, http.MethodPost, "/admin/credentials", "", map[string]any{
		"project_id": projectID,
		"kind":       "github_webhook_secret",
		"name":       "core-webhook",
		"value":      "super-secret-webhook-value-123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create credential status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret-webhook-value-123") {
		t.Fatal("response leaked the plaintext credential")
	}
	cred := body["data"].(map[string]any)
	credID := cred["id"].(string)
	if cred["mask"] != "****-123" {
		t.Fatalf("unexpected mask %v", cred["mask"])
	}

	rec, _ = do(t, app, http.MethodPost, "/admin/credentials/"+credID+"/test", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("test credential status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"ok\":true") {
		t.Fatalf("expected ok=true: %s", rec.Body.String())
	}

	rec, listBody := do(t, app, http.MethodGet, "/admin/credentials?project_id="+projectID, "", nil)
	items, _ := listBody["data"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list credentials: %d %v", rec.Code, listBody)
	}
	if strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatal("list leaked plaintext")
	}

	rec, _ = do(t, app, http.MethodPost, "/admin/credentials/"+credID+"/rotate", "", map[string]any{"value": "rotated-value-987654"})
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d: %s", rec.Code, rec.Body.String())
	}

	rec, auditBody := do(t, app, http.MethodGet, "/admin/audit?limit=20", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status = %d", rec.Code)
	}
	entries, _ := auditBody["data"].([]any)
	if len(entries) < 3 {
		t.Fatalf("expected at least 3 audit entries, got %d", len(entries))
	}
	for _, e := range entries {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "super-secret") || strings.Contains(string(raw), "rotated-value") {
			t.Fatal("audit log leaked credential material")
		}
	}

	rec, _ = do(t, app, http.MethodDelete, "/admin/credentials/"+credID, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
}

func TestAuthRequiredWhenTokenConfigured(t *testing.T) {
	app, _ := newTestAPI(t, "s3cr3t-token")

	rec, _ := do(t, app, http.MethodGet, "/admin/projects", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	rec, _ = do(t, app, http.MethodGet, "/admin/projects", "s3cr3t-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestValidationErrors(t *testing.T) {
	app, _ := newTestAPI(t, "")

	rec, _ := do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{"name": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name should be 400, got %d", rec.Code)
	}

	rec, _ = do(t, app, http.MethodPost, "/admin/credentials", "", map[string]any{
		"kind": "not_a_kind", "name": "x", "value": "y",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind should be 400, got %d", rec.Code)
	}

	rec, _ = do(t, app, http.MethodGet, "/admin/projects/prj_missing", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing project should be 404, got %d", rec.Code)
	}
}

var _ = time.Now
