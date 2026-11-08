package adminapi

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

const maxCodexConfigBytes = 1 << 20

var supportedReasoningEfforts = map[string]bool{
	"": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true,
}

type modelConfigImportRequest struct {
	Source  string `json:"source"`
	Content string `json:"content"`
}

type codexConfigFile struct {
	Model                string                       `toml:"model"`
	ModelProvider        string                       `toml:"model_provider"`
	ModelReasoningEffort string                       `toml:"model_reasoning_effort"`
	ReviewModel          string                       `toml:"review_model"`
	ModelProviders       map[string]codexProviderFile `toml:"model_providers"`
}

type codexProviderFile struct {
	Name                    string `toml:"name"`
	BaseURL                 string `toml:"base_url"`
	WireAPI                 string `toml:"wire_api"`
	EnvKey                  string `toml:"env_key"`
	APIKey                  string `toml:"api_key"`
	ExperimentalBearerToken string `toml:"experimental_bearer_token"`
	RequiresOpenAIAuth      bool   `toml:"requires_openai_auth"`
}

type parsedModelConfig struct {
	ProviderKey     string
	Model           string
	ReviewModel     string
	ReasoningEffort string
	Providers       []parsedModelProvider
	Warnings        []string
}

type parsedModelProvider struct {
	Key                string
	Name               string
	BaseURL            string
	WireAPI            string
	APIKey             string
	EnvKey             string
	RequiresOpenAIAuth bool
	Models             []string
}

type modelConfigImportProviderView struct {
	Key                   string   `json:"key"`
	Name                  string   `json:"name"`
	BaseURL               string   `json:"base_url"`
	WireAPI               string   `json:"wire_api"`
	HasAPIKey             bool     `json:"has_api_key"`
	HasExistingCredential bool     `json:"has_existing_credential"`
	EnvKey                string   `json:"env_key"`
	RequiresOpenAIAuth    bool     `json:"requires_openai_auth"`
	Models                []string `json:"models"`
	Action                string   `json:"action"`
}

type modelConfigImportPreview struct {
	ProviderCount   int                             `json:"provider_count"`
	CreateCount     int                             `json:"create_count"`
	UpdateCount     int                             `json:"update_count"`
	Providers       []modelConfigImportProviderView `json:"providers"`
	DefaultModel    string                          `json:"default_model"`
	ReasoningEffort string                          `json:"reasoning_effort"`
	Warnings        []string                        `json:"warnings"`
}

type modelConfigImportResult struct {
	Source    string                `json:"source"`
	Imported  int                   `json:"imported"`
	Updated   int                   `json:"updated"`
	Providers []store.ModelProvider `json:"providers"`
	Default   modelDefaultView      `json:"default"`
	Warnings  []string              `json:"warnings"`
}

type modelDefaultView struct {
	ProviderID      string `json:"provider_id"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func (a *API) previewModelConfigImport(c iris.Context) {
	var in modelConfigImportRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	parsed, err := parseCodexModelConfig(in.Content)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	existing, err := a.store.ListModelProviders(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: buildModelConfigImportPreview(parsed, existing)})
}

func (a *API) importModelConfig(c iris.Context) {
	var in modelConfigImportRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	parsed, err := parseCodexModelConfig(in.Content)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	ctx := c.Request().Context()
	providers, imported, updated, warnings, err := a.applyCodexModelConfig(ctx, parsed)
	if err != nil {
		a.fail(c, err)
		return
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "config.toml"
	}
	warnings = append(warnings, parsed.Warnings...)
	defaults := modelDefaultView{}
	if len(providers) > 0 {
		defaults = a.importedModelDefault(ctx, parsed, providers, &warnings)
	}
	a.audit(ctx, c, "model.config.import", "model_provider", source, nil, "ok", map[string]any{
		"source": source, "imported": imported, "updated": updated,
	})
	_ = c.JSON(envelope{Data: modelConfigImportResult{
		Source: source, Imported: imported, Updated: updated,
		Providers: providers, Default: defaults, Warnings: warnings,
	}})
}

func parseCodexModelConfig(content string) (*parsedModelConfig, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("config.toml 内容为空")
	}
	if len(content) > maxCodexConfigBytes {
		return nil, fmt.Errorf("config.toml 超过 1 MB，已拒绝导入")
	}
	var doc codexConfigFile
	if _, err := toml.Decode(content, &doc); err != nil {
		return nil, fmt.Errorf("解析 config.toml 失败：%v", err)
	}
	if len(doc.ModelProviders) == 0 {
		return nil, fmt.Errorf("config.toml 中没有 [model_providers.*] 配置")
	}
	if len(doc.ModelProviders) > 50 {
		return nil, fmt.Errorf("一次最多导入 50 个模型供应商")
	}

	out := &parsedModelConfig{
		ProviderKey:     strings.TrimSpace(doc.ModelProvider),
		Model:           strings.TrimSpace(doc.Model),
		ReviewModel:     strings.TrimSpace(doc.ReviewModel),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(doc.ModelReasoningEffort)),
	}
	keys := make([]string, 0, len(doc.ModelProviders))
	for key := range doc.ModelProviders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seenNames := map[string]string{}
	selectedFound := false
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		item := doc.ModelProviders[rawKey]
		if key == "" {
			return nil, fmt.Errorf("model_providers 中存在空供应商 ID")
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = key
		}
		if previous, duplicate := seenNames[strings.ToLower(name)]; duplicate {
			return nil, fmt.Errorf("供应商 %q 与 %q 使用了相同名称 %q", key, previous, name)
		}
		seenNames[strings.ToLower(name)] = key
		baseURL := strings.TrimRight(strings.TrimSpace(item.BaseURL), "/")
		if baseURL == "" {
			return nil, fmt.Errorf("供应商 %q 缺少 base_url", name)
		}
		u, err := url.ParseRequestURI(baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("供应商 %q 的 base_url 不是有效的 HTTP/HTTPS 地址", name)
		}
		wireAPI := strings.ToLower(strings.TrimSpace(item.WireAPI))
		if wireAPI == "" {
			wireAPI = "responses"
		}
		if wireAPI != "responses" && wireAPI != "chat" {
			return nil, fmt.Errorf("供应商 %q 的 wire_api=%q 不受支持，请使用 responses 或 chat", name, wireAPI)
		}
		apiKey := strings.TrimSpace(item.ExperimentalBearerToken)
		if apiKey == "" {
			apiKey = strings.TrimSpace(item.APIKey)
		}
		if strings.HasPrefix(apiKey, "http://") || strings.HasPrefix(apiKey, "https://") {
			return nil, fmt.Errorf("供应商 %q 的密钥位置填成了 URL，请检查配置", name)
		}
		envKey := strings.TrimSpace(item.EnvKey)
		if envKey != "" && apiKey == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("供应商 %q 使用 env_key=%s；浏览器不会读取本机环境变量，导入后需要手动补充 API Key。", name, envKey))
		}
		if item.RequiresOpenAIAuth && apiKey == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("供应商 %q 依赖 OpenAI 登录态；SmartPipe 运行时需要单独的 API Key，导入后请补充。", name))
		}
		provider := parsedModelProvider{
			Key: key, Name: name, BaseURL: baseURL, WireAPI: wireAPI,
			APIKey: apiKey, EnvKey: envKey, RequiresOpenAIAuth: item.RequiresOpenAIAuth,
			Models: []string{},
		}
		if key == out.ProviderKey {
			selectedFound = true
			provider.Models = uniqueModelIDs(out.Model, out.ReviewModel)
		}
		out.Providers = append(out.Providers, provider)
	}
	if out.ProviderKey == "" {
		out.Warnings = append(out.Warnings, "没有设置顶层 model_provider，将只导入供应商，不切换默认模型。")
		out.Model = ""
		out.ReviewModel = ""
	} else if !selectedFound {
		out.Warnings = append(out.Warnings, fmt.Sprintf("model_provider=%q 没有对应的 [model_providers.%s]，默认模型不会切换。", out.ProviderKey, out.ProviderKey))
		out.Model = ""
		out.ReviewModel = ""
	}
	if !supportedReasoningEfforts[out.ReasoningEffort] {
		out.Warnings = append(out.Warnings, fmt.Sprintf("推理强度 %q 不在当前支持范围内，已忽略。", out.ReasoningEffort))
		out.ReasoningEffort = ""
	}
	return out, nil
}

func uniqueModelIDs(values ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func buildModelConfigImportPreview(parsed *parsedModelConfig, existing []store.ModelProvider) modelConfigImportPreview {
	byName := make(map[string]*store.ModelProvider, len(existing))
	for i := range existing {
		byName[strings.ToLower(existing[i].Name)] = &existing[i]
	}
	out := modelConfigImportPreview{
		ProviderCount: len(parsed.Providers), Providers: make([]modelConfigImportProviderView, 0, len(parsed.Providers)),
		DefaultModel: parsed.Model, ReasoningEffort: parsed.ReasoningEffort, Warnings: append([]string(nil), parsed.Warnings...),
	}
	for _, provider := range parsed.Providers {
		match := byName[strings.ToLower(provider.Name)]
		action := "create"
		if match != nil {
			action = "update"
			out.UpdateCount++
		} else {
			out.CreateCount++
		}
		view := modelConfigImportProviderView{
			Key: provider.Key, Name: provider.Name, BaseURL: provider.BaseURL, WireAPI: provider.WireAPI,
			HasAPIKey: provider.APIKey != "", EnvKey: provider.EnvKey, RequiresOpenAIAuth: provider.RequiresOpenAIAuth,
			Models: append([]string(nil), provider.Models...), Action: action,
		}
		if match != nil && match.CredentialID != "" {
			view.HasExistingCredential = true
		}
		out.Providers = append(out.Providers, view)
	}
	return out
}

func (a *API) applyCodexModelConfig(ctx context.Context, parsed *parsedModelConfig) ([]store.ModelProvider, int, int, []string, error) {
	existing, err := a.store.ListModelProviders(ctx)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	byName := make(map[string]*store.ModelProvider, len(existing))
	for i := range existing {
		byName[strings.ToLower(existing[i].Name)] = &existing[i]
	}
	result := make([]store.ModelProvider, 0, len(parsed.Providers))
	createdProviders := make([]string, 0, len(parsed.Providers))
	createdCredentials := make([]string, 0, len(parsed.Providers))
	importedCount, updatedCount := 0, 0
	warnings := make([]string, 0)
	cleanup := func() {
		for _, id := range createdProviders {
			_ = a.store.DeleteModelProvider(ctx, id)
		}
		for _, id := range createdCredentials {
			_ = a.store.DeleteCredential(ctx, id)
		}
	}

	for _, item := range parsed.Providers {
		match := byName[strings.ToLower(item.Name)]
		var provider *store.ModelProvider
		if match == nil {
			p, err := a.store.CreateModelProvider(ctx, &store.ModelProvider{
				Name: item.Name, BaseURL: item.BaseURL, WireAPI: item.WireAPI, Enabled: true,
			})
			if err != nil {
				cleanup()
				return nil, importedCount, updatedCount, warnings, err
			}
			createdProviders = append(createdProviders, p.ID)
			provider = p
			importedCount++
		} else {
			provider = match
			provider.Name = item.Name
			provider.BaseURL = item.BaseURL
			provider.WireAPI = item.WireAPI
			provider.Enabled = true
			updatedCount++
		}

		meta := map[string]any{
			"base_url": provider.BaseURL, "provider_name": provider.Name, "source": "codex-config",
		}
		if item.APIKey != "" {
			if provider.CredentialID == "" {
				cred, err := a.store.CreateCredential(ctx, nil, store.CredentialModelProvider, "model-provider/"+provider.ID, item.APIKey, meta)
				if err != nil {
					cleanup()
					return nil, importedCount, updatedCount, warnings, err
				}
				provider.CredentialID = cred.ID
				createdCredentials = append(createdCredentials, cred.ID)
			} else {
				if _, err := a.store.RotateCredential(ctx, provider.CredentialID, item.APIKey); err != nil {
					cleanup()
					return nil, importedCount, updatedCount, warnings, err
				}
				_ = a.store.UpdateCredentialMetadata(ctx, provider.CredentialID, meta)
			}
		}
		updated, err := a.store.UpdateModelProvider(ctx, provider)
		if err != nil {
			cleanup()
			return nil, importedCount, updatedCount, warnings, err
		}
		for _, modelID := range item.Models {
			if _, err := a.store.AddProviderModel(ctx, updated.ID, modelID, true); err != nil {
				cleanup()
				return nil, importedCount, updatedCount, warnings, err
			}
		}
		updated, _ = a.store.GetModelProvider(ctx, updated.ID)
		result = append(result, *updated)
		if item.APIKey == "" && updated.CredentialID == "" {
			warnings = append(warnings, fmt.Sprintf("供应商 %q 已导入连接信息，但还没有可用 API Key，测试或运行前需要手动补充。", updated.Name))
		}
	}
	return result, importedCount, updatedCount, warnings, nil
}

func (a *API) importedModelDefault(ctx context.Context, parsed *parsedModelConfig, providers []store.ModelProvider, warnings *[]string) modelDefaultView {
	if parsed.ProviderKey == "" || parsed.Model == "" {
		return modelDefaultView{}
	}
	var selected *store.ModelProvider
	for i := range providers {
		for _, item := range parsed.Providers {
			if item.Key == parsed.ProviderKey && item.Name == providers[i].Name {
				selected = &providers[i]
				break
			}
		}
	}
	if selected == nil {
		return modelDefaultView{}
	}
	if selected.CredentialID == "" {
		*warnings = append(*warnings, fmt.Sprintf("默认模型 %q 未切换：供应商 %q 缺少 API Key。", parsed.Model, selected.Name))
		return modelDefaultView{}
	}
	values := map[string]string{
		"codex_default_provider": selected.ID,
		"codex_default_model":    parsed.Model,
		"codex_default_effort":   parsed.ReasoningEffort,
	}
	for key, value := range values {
		if err := a.store.SetSetting(ctx, key, value); err != nil {
			*warnings = append(*warnings, "默认模型保存失败："+err.Error())
			return modelDefaultView{}
		}
	}
	return modelDefaultView{ProviderID: selected.ID, Model: parsed.Model, ReasoningEffort: parsed.ReasoningEffort}
}
