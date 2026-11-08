package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const sampleCodexConfig = `
model = "deepseek-v4-flash"
model_provider = "deepseek"
model_reasoning_effort = "high"
review_model = "deepseek-v4-pro"

[model_providers.deepseek]
name = "DeepSeek"
base_url = "https://api.deepseek.com/"
wire_api = "responses"
experimental_bearer_token = "sk-import-test-secret"

[model_providers.teamorouter]
name = "TeamoRouter"
base_url = "https://api.teamorouter.com/v1"
wire_api = "responses"
env_key = "TEAMO_API_KEY"
requires_openai_auth = true
`

func TestParseCodexModelConfig(t *testing.T) {
	parsed, err := parseCodexModelConfig(sampleCodexConfig)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if parsed.ProviderKey != "deepseek" || parsed.Model != "deepseek-v4-flash" || parsed.ReasoningEffort != "high" {
		t.Fatalf("unexpected defaults: %#v", parsed)
	}
	if len(parsed.Providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(parsed.Providers))
	}
	if got := parsed.Providers[0].Models; len(got) != 2 || got[0] != "deepseek-v4-flash" || got[1] != "deepseek-v4-pro" {
		t.Fatalf("selected provider models = %#v", got)
	}
	if len(parsed.Providers[1].Models) != 0 {
		t.Fatalf("unselected provider should not receive selected models: %#v", parsed.Providers[1].Models)
	}
	view := buildModelConfigImportPreview(parsed, nil)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "sk-import-test-secret") {
		t.Fatal("preview leaked the API key")
	}
	if !strings.Contains(string(raw), "TEAMO_API_KEY") {
		t.Fatal("preview should show the env_key that still needs a secret")
	}
}

func TestParseCodexModelConfigRejectsInvalidProvider(t *testing.T) {
	_, err := parseCodexModelConfig(`
model_provider = "bad"
[model_providers.bad]
name = "Bad"
base_url = "file:///tmp/key"
`)
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("expected base_url error, got %v", err)
	}
}

func TestModelConfigImportAPI(t *testing.T) {
	app, st := newTestAPI(t, "")
	ctx := context.Background()
	cleanupModels := func() {
		_, _ = st.DB().Exec(`DELETE FROM provider_models`)
		_, _ = st.DB().Exec(`DELETE FROM model_providers`)
		_, _ = st.DB().Exec(`DELETE FROM runtime_settings WHERE key IN ('codex_default_provider','codex_default_model','codex_default_effort')`)
		_, _ = st.DB().Exec(`DELETE FROM credentials WHERE name LIKE 'model-provider/%'`)
	}
	cleanupModels()
	t.Cleanup(cleanupModels)

	rec, body := do(t, app, http.MethodPost, "/admin/model-config/import/preview", "", map[string]any{
		"source": "~/.codex/config.toml", "content": sampleCodexConfig,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-import-test-secret") {
		t.Fatal("preview response leaked the API key")
	}
	preview := body["data"].(map[string]any)
	if preview["create_count"] != float64(2) || preview["default_model"] != "deepseek-v4-flash" {
		t.Fatalf("unexpected preview: %#v", preview)
	}

	rec, body = do(t, app, http.MethodPost, "/admin/model-config/import", "", map[string]any{
		"source": "~/.codex/config.toml", "content": sampleCodexConfig,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-import-test-secret") {
		t.Fatal("import response leaked the API key")
	}
	result := body["data"].(map[string]any)
	if result["imported"] != float64(2) {
		t.Fatalf("unexpected import result: %#v", result)
	}

	rec, body = do(t, app, http.MethodGet, "/admin/model-providers", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list providers status = %d: %s", rec.Code, rec.Body.String())
	}
	providers := body["data"].([]any)
	if len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(providers))
	}
	var deepseek map[string]any
	var deepseekModels int
	for _, raw := range providers {
		provider := raw.(map[string]any)
		if provider["name"] == "DeepSeek" {
			deepseek = provider
			deepseekModels = len(provider["models"].([]any))
		}
	}
	if deepseek == nil || deepseekModels != 2 || deepseek["credential_id"] == "" {
		t.Fatalf("DeepSeek provider not imported correctly: %#v", deepseek)
	}

	rec, body = do(t, app, http.MethodGet, "/admin/model-default", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("default status = %d: %s", rec.Code, rec.Body.String())
	}
	defaults := body["data"].(map[string]any)
	if defaults["provider_id"] != deepseek["id"] || defaults["model"] != "deepseek-v4-flash" || defaults["reasoning_effort"] != "high" {
		t.Fatalf("unexpected default: %#v", defaults)
	}

	rec, body = do(t, app, http.MethodPost, "/admin/model-config/import", "", map[string]any{
		"source": "~/.codex/config.toml", "content": sampleCodexConfig,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("second import status = %d: %s", rec.Code, rec.Body.String())
	}
	result = body["data"].(map[string]any)
	if result["updated"] != float64(2) || result["imported"] != float64(0) {
		t.Fatalf("second import should update existing providers: %#v", result)
	}
	var credentialCount int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE name LIKE 'model-provider/%'`).Scan(&credentialCount); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if credentialCount != 1 {
		t.Fatalf("credential count = %d, want 1 after re-import", credentialCount)
	}
}
