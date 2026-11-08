package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const sampleCodexConfig = `
profile = "qax"
model = "deepseek-v4-flash"
model_provider = "qax"
model_reasoning_effort = "high"

[profiles.qax]
model = "deepseek-v4-flash"
model_provider = "qax"
model_reasoning_effort = "high"

[profiles.review]
model = "deepseek-v4-pro"
model_provider = "qax"
model_reasoning_effort = "medium"

[model_providers.deepseek]
name = "DeepSeek"
base_url = "https://api.deepseek.com/"
wire_api = "responses"
experimental_bearer_token = "sk-import-test-secret"

[model_providers.qax]
name = "qax"
base_url = "http://127.0.0.1:40001/v1"
wire_api = "responses"
experimental_bearer_token = "sk-import-qax-secret"

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
	if parsed.DefaultProfileKey != "qax" {
		t.Fatalf("default profile = %q, want qax", parsed.DefaultProfileKey)
	}
	if len(parsed.Profiles) != 2 || len(parsed.Providers) != 3 {
		t.Fatalf("profiles=%d providers=%d", len(parsed.Profiles), len(parsed.Providers))
	}
	var qaxProvider *parsedModelProvider
	for i := range parsed.Providers {
		if parsed.Providers[i].Key == "qax" {
			qaxProvider = &parsed.Providers[i]
		}
	}
	if qaxProvider == nil || len(qaxProvider.Models) != 2 {
		t.Fatalf("qax provider models = %#v", qaxProvider)
	}
	view := buildModelConfigImportPreview(parsed, nil, nil)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "sk-import-test-secret") || strings.Contains(string(raw), "sk-import-qax-secret") {
		t.Fatal("preview leaked an API key")
	}
	if !strings.Contains(string(raw), "TEAMO_API_KEY") {
		t.Fatal("preview should show the env_key that still needs a secret")
	}
	if view.ProfileCount != 2 || view.ProviderCount != 3 || view.DefaultProfileKey != "qax" {
		t.Fatalf("unexpected preview: %#v", view)
	}
}

func TestParseCodexModelConfigRejectsInvalidProvider(t *testing.T) {
	_, err := parseCodexModelConfig(`
model_provider = "bad"
model = "m"
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
		_, _ = st.DB().Exec(`DELETE FROM model_profiles`)
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
	if preview["profile_count"] != float64(2) || preview["provider_count"] != float64(3) || preview["default_profile_key"] != "qax" {
		t.Fatalf("unexpected preview: %#v", preview)
	}

	rec, body = do(t, app, http.MethodPost, "/admin/model-config/import", "", map[string]any{
		"source": "~/.codex/config.toml", "content": sampleCodexConfig,
		"profile_keys": []string{"qax"}, "provider_keys": []string{"deepseek"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-import-test-secret") || strings.Contains(rec.Body.String(), "sk-import-qax-secret") {
		t.Fatal("import response leaked an API key")
	}
	result := body["data"].(map[string]any)
	if result["imported_profiles"] != float64(1) || result["imported_providers"] != float64(2) {
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
	var qaxProvider map[string]any
	for _, raw := range providers {
		provider := raw.(map[string]any)
		if provider["name"] == "qax" {
			qaxProvider = provider
		}
	}
	if qaxProvider == nil || qaxProvider["credential_id"] == "" {
		t.Fatalf("qax provider not imported correctly: %#v", qaxProvider)
	}

	rec, body = do(t, app, http.MethodGet, "/admin/model-profiles", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list profiles status = %d: %s", rec.Code, rec.Body.String())
	}
	profiles := body["data"].([]any)
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
	profile := profiles[0].(map[string]any)
	if profile["name"] != "qax" || profile["model"] != "deepseek-v4-flash" || profile["provider_name"] != "qax" {
		t.Fatalf("unexpected imported profile: %#v", profile)
	}

	rec, body = do(t, app, http.MethodGet, "/admin/model-default", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("default status = %d: %s", rec.Code, rec.Body.String())
	}
	defaults := body["data"].(map[string]any)
	if defaults["model"] != "deepseek-v4-flash" || defaults["reasoning_effort"] != "high" || defaults["provider_id"] == "" {
		t.Fatalf("unexpected default: %#v", defaults)
	}

	rec, body = do(t, app, http.MethodPost, "/admin/model-config/import", "", map[string]any{
		"source": "~/.codex/config.toml", "content": sampleCodexConfig,
		"profile_keys": []string{"qax"}, "provider_keys": []string{"qax"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("second import status = %d: %s", rec.Code, rec.Body.String())
	}
	result = body["data"].(map[string]any)
	if result["updated_profiles"] != float64(1) || result["imported_profiles"] != float64(0) || result["updated_providers"] != float64(1) {
		t.Fatalf("second import should update existing records: %#v", result)
	}
	var credentialCount int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE name LIKE 'model-provider/%'`).Scan(&credentialCount); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if credentialCount != 2 {
		t.Fatalf("credential count = %d, want 2 after re-import", credentialCount)
	}
}
