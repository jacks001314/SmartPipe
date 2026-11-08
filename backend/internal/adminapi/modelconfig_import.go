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
	Source       string   `json:"source"`
	Content      string   `json:"content"`
	ProfileKeys  []string `json:"profile_keys"`
	ProviderKeys []string `json:"provider_keys"`
}

type codexConfigFile struct {
	Profile              string                       `toml:"profile"`
	Model                string                       `toml:"model"`
	ModelProvider        string                       `toml:"model_provider"`
	ModelReasoningEffort string                       `toml:"model_reasoning_effort"`
	Profiles             map[string]codexProfileFile  `toml:"profiles"`
	ModelProviders       map[string]codexProviderFile `toml:"model_providers"`
}

type codexProfileFile struct {
	Model                string `toml:"model"`
	ModelProvider        string `toml:"model_provider"`
	ModelReasoningEffort string `toml:"model_reasoning_effort"`
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
	DefaultProfileKey string
	Profiles          []parsedModelProfile
	Providers         []parsedModelProvider
	Warnings          []string
}

type parsedModelProfile struct {
	Key             string
	Model           string
	ProviderKey     string
	ReasoningEffort string
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
	Key                   string `json:"key"`
	Name                  string `json:"name"`
	BaseURL               string `json:"base_url"`
	WireAPI               string `json:"wire_api"`
	APIKeySource          string `json:"api_key_source"`
	HasAPIKey             bool   `json:"has_api_key"`
	HasExistingCredential bool   `json:"has_existing_credential"`
	EnvKey                string `json:"env_key"`
	RequiresOpenAIAuth    bool   `json:"requires_openai_auth"`
	Action                string `json:"action"`
}

type modelConfigImportProfileView struct {
	Key             string `json:"key"`
	Model           string `json:"model"`
	ProviderKey     string `json:"provider_key"`
	ProviderName    string `json:"provider_name"`
	ReasoningEffort string `json:"reasoning_effort"`
	APIKeySource    string `json:"api_key_source"`
	Action          string `json:"action"`
	ProviderMissing bool   `json:"provider_missing"`
}

type modelConfigImportPreview struct {
	ProfileCount      int                             `json:"profile_count"`
	ProviderCount     int                             `json:"provider_count"`
	DefaultProfileKey string                          `json:"default_profile_key"`
	Profiles          []modelConfigImportProfileView  `json:"profiles"`
	Providers         []modelConfigImportProviderView `json:"providers"`
	Warnings          []string                        `json:"warnings"`
}

type modelConfigImportResult struct {
	Source            string                `json:"source"`
	ImportedProfiles  int                   `json:"imported_profiles"`
	UpdatedProfiles   int                   `json:"updated_profiles"`
	ImportedProviders int                   `json:"imported_providers"`
	UpdatedProviders  int                   `json:"updated_providers"`
	Profiles          []store.ModelProfile  `json:"profiles"`
	Providers         []store.ModelProvider `json:"providers"`
	Default           modelDefaultView      `json:"default"`
	Warnings          []string              `json:"warnings"`
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
	ctx := c.Request().Context()
	providers, err := a.store.ListModelProviders(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: buildModelConfigImportPreview(parsed, providers, profiles)})
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
	if len(in.ProfileKeys) == 0 && len(in.ProviderKeys) == 0 {
		a.badRequest(c, "请至少选择一个 Profile 或 Provider")
		return
	}
	ctx := c.Request().Context()
	result, err := a.applyCodexModelConfig(ctx, parsed, in.ProfileKeys, in.ProviderKeys)
	if err != nil {
		if _, ok := err.(*modelImportSelectionError); ok {
			a.badRequest(c, err.Error())
		} else {
			a.fail(c, err)
		}
		return
	}
	result.Source = strings.TrimSpace(in.Source)
	if result.Source == "" {
		result.Source = "config.toml"
	}
	a.audit(ctx, c, "model.config.import", "model_provider", result.Source, nil, "ok", map[string]any{
		"source": result.Source, "imported_profiles": result.ImportedProfiles,
		"updated_profiles": result.UpdatedProfiles, "imported_providers": result.ImportedProviders,
		"updated_providers": result.UpdatedProviders,
	})
	_ = c.JSON(envelope{Data: result})
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

	out := &parsedModelConfig{}
	providerIndexByKey := map[string]int{}
	keys := make([]string, 0, len(doc.ModelProviders))
	for key := range doc.ModelProviders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seenNames := map[string]string{}
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
		provider := parsedModelProvider{
			Key: key, Name: name, BaseURL: baseURL, WireAPI: wireAPI,
			APIKey: apiKey, EnvKey: strings.TrimSpace(item.EnvKey),
			RequiresOpenAIAuth: item.RequiresOpenAIAuth, Models: []string{},
		}
		out.Providers = append(out.Providers, provider)
		providerIndexByKey[key] = len(out.Providers) - 1
	}

	profileNames := make([]string, 0, len(doc.Profiles))
	for name := range doc.Profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	seenProfiles := map[string]bool{}
	for _, rawName := range profileNames {
		name := strings.TrimSpace(rawName)
		item := doc.Profiles[rawName]
		if name == "" || seenProfiles[strings.ToLower(name)] {
			continue
		}
		model := strings.TrimSpace(item.Model)
		if model == "" {
			model = strings.TrimSpace(doc.Model)
		}
		providerKey := strings.TrimSpace(item.ModelProvider)
		if providerKey == "" {
			providerKey = strings.TrimSpace(doc.ModelProvider)
		}
		effort := strings.ToLower(strings.TrimSpace(item.ModelReasoningEffort))
		if effort == "" {
			effort = strings.ToLower(strings.TrimSpace(doc.ModelReasoningEffort))
		}
		if model == "" || providerKey == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Profile %q 缺少 model 或 model_provider，已跳过。", name))
			continue
		}
		if !supportedReasoningEfforts[effort] {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Profile %q 的推理强度 %q 不受支持，已忽略该强度。", name, effort))
			effort = ""
		}
		out.Profiles = append(out.Profiles, parsedModelProfile{
			Key: name, Model: model, ProviderKey: providerKey, ReasoningEffort: effort,
		})
		seenProfiles[strings.ToLower(name)] = true
	}

	defaultKey := strings.TrimSpace(doc.Profile)
	baseModel := strings.TrimSpace(doc.Model)
	baseProvider := strings.TrimSpace(doc.ModelProvider)
	if baseModel != "" && baseProvider != "" {
		syntheticName := defaultKey
		if syntheticName == "" {
			syntheticName = baseProvider
		}
		if !seenProfiles[strings.ToLower(syntheticName)] {
			effort := strings.ToLower(strings.TrimSpace(doc.ModelReasoningEffort))
			if !supportedReasoningEfforts[effort] {
				out.Warnings = append(out.Warnings, fmt.Sprintf("默认 Profile 的推理强度 %q 不受支持，已忽略。", effort))
				effort = ""
			}
			out.Profiles = append(out.Profiles, parsedModelProfile{
				Key: syntheticName, Model: baseModel, ProviderKey: baseProvider, ReasoningEffort: effort,
			})
			seenProfiles[strings.ToLower(syntheticName)] = true
		}
		if defaultKey == "" {
			defaultKey = syntheticName
		}
	}
	if defaultKey != "" && !seenProfiles[strings.ToLower(defaultKey)] {
		out.Warnings = append(out.Warnings, fmt.Sprintf("默认 profile=%q 在配置中没有对应定义，导入后不会自动切换默认模型。", defaultKey))
		defaultKey = ""
	}
	out.DefaultProfileKey = defaultKey

	for _, profile := range out.Profiles {
		providerIndex, ok := providerIndexByKey[profile.ProviderKey]
		if !ok {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Profile %q 引用了不存在的供应商 %q，无法导入该 Profile。", profile.Key, profile.ProviderKey))
			continue
		}
		out.Providers[providerIndex].Models = append(out.Providers[providerIndex].Models, profile.Model)
	}
	for i := range out.Providers {
		out.Providers[i].Models = uniqueModelIDs(out.Providers[i].Models...)
	}
	if len(out.Profiles) == 0 {
		out.Warnings = append(out.Warnings, "配置中没有可导入的 Profile；仍可单独导入 Provider。")
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

func providerAPIKeySource(provider parsedModelProvider, existing *store.ModelProvider) string {
	if provider.APIKey != "" {
		return "toml"
	}
	if existing != nil && existing.CredentialID != "" {
		return "saved"
	}
	if provider.EnvKey != "" {
		return "env"
	}
	return "missing"
}

func buildModelConfigImportPreview(parsed *parsedModelConfig, existingProviders []store.ModelProvider, existingProfiles []store.ModelProfile) modelConfigImportPreview {
	providersByName := make(map[string]*store.ModelProvider, len(existingProviders))
	for i := range existingProviders {
		providersByName[strings.ToLower(existingProviders[i].Name)] = &existingProviders[i]
	}
	providersByKey := make(map[string]parsedModelProvider, len(parsed.Providers))
	for _, provider := range parsed.Providers {
		providersByKey[provider.Key] = provider
	}
	profilesByName := make(map[string]*store.ModelProfile, len(existingProfiles))
	for i := range existingProfiles {
		profilesByName[strings.ToLower(existingProfiles[i].Name)] = &existingProfiles[i]
	}

	out := modelConfigImportPreview{
		ProfileCount: len(parsed.Profiles), ProviderCount: len(parsed.Providers),
		DefaultProfileKey: parsed.DefaultProfileKey, Warnings: append([]string(nil), parsed.Warnings...),
		Profiles:  make([]modelConfigImportProfileView, 0, len(parsed.Profiles)),
		Providers: make([]modelConfigImportProviderView, 0, len(parsed.Providers)),
	}
	for _, provider := range parsed.Providers {
		existing := providersByName[strings.ToLower(provider.Name)]
		source := providerAPIKeySource(provider, existing)
		action := "create"
		if existing != nil {
			action = "update"
		}
		view := modelConfigImportProviderView{
			Key: provider.Key, Name: provider.Name, BaseURL: provider.BaseURL, WireAPI: provider.WireAPI,
			APIKeySource: source, HasAPIKey: provider.APIKey != "", EnvKey: provider.EnvKey,
			RequiresOpenAIAuth: provider.RequiresOpenAIAuth, Action: action,
		}
		if existing != nil && existing.CredentialID != "" {
			view.HasExistingCredential = true
		}
		if source == "env" || source == "missing" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("供应商 %q 没有可直接导入的 API Key（来源：%s），运行前需要补充。", provider.Name, source))
		}
		out.Providers = append(out.Providers, view)
	}
	for _, profile := range parsed.Profiles {
		provider, providerOK := providersByKey[profile.ProviderKey]
		existingProvider := providersByName[strings.ToLower(provider.Name)]
		source := "missing"
		if providerOK {
			source = providerAPIKeySource(provider, existingProvider)
		}
		action := "create"
		if profilesByName[strings.ToLower(profile.Key)] != nil {
			action = "update"
		}
		out.Profiles = append(out.Profiles, modelConfigImportProfileView{
			Key: profile.Key, Model: profile.Model, ProviderKey: profile.ProviderKey, ProviderName: provider.Name,
			ReasoningEffort: profile.ReasoningEffort, APIKeySource: source, Action: action, ProviderMissing: !providerOK,
		})
	}
	return out
}

type modelImportSelectionError struct{ message string }

func (e *modelImportSelectionError) Error() string { return e.message }

func (a *API) applyCodexModelConfig(ctx context.Context, parsed *parsedModelConfig, profileKeys, providerKeys []string) (*modelConfigImportResult, error) {
	existingProviders, err := a.store.ListModelProviders(ctx)
	if err != nil {
		return nil, err
	}
	existingProfiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		return nil, err
	}
	providersByName := make(map[string]*store.ModelProvider, len(existingProviders))
	for i := range existingProviders {
		providersByName[strings.ToLower(existingProviders[i].Name)] = &existingProviders[i]
	}
	profilesByName := make(map[string]*store.ModelProfile, len(existingProfiles))
	for i := range existingProfiles {
		profilesByName[strings.ToLower(existingProfiles[i].Name)] = &existingProfiles[i]
	}
	providerByKey := make(map[string]parsedModelProvider, len(parsed.Providers))
	for _, provider := range parsed.Providers {
		providerByKey[provider.Key] = provider
	}
	profileByKey := make(map[string]parsedModelProfile, len(parsed.Profiles))
	for _, profile := range parsed.Profiles {
		if _, ok := providerByKey[profile.ProviderKey]; ok {
			profileByKey[profile.Key] = profile
		}
	}

	selectedProviders := map[string]bool{}
	for _, key := range providerKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := providerByKey[key]; !ok {
			return nil, &modelImportSelectionError{message: fmt.Sprintf("Provider %q 不存在于当前配置文件", key)}
		}
		selectedProviders[key] = true
	}
	selectedProfiles := map[string]bool{}
	for _, key := range profileKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		profile, ok := profileByKey[key]
		if !ok {
			return nil, &modelImportSelectionError{message: fmt.Sprintf("Profile %q 不存在、缺少供应商，或引用了不存在的 Provider", key)}
		}
		selectedProfiles[key] = true
		selectedProviders[profile.ProviderKey] = true
	}
	if len(selectedProviders) == 0 && len(selectedProfiles) == 0 {
		return nil, &modelImportSelectionError{message: "请至少选择一个 Profile 或 Provider"}
	}

	result := &modelConfigImportResult{
		Profiles: []store.ModelProfile{}, Providers: []store.ModelProvider{},
		Warnings: append([]string(nil), parsed.Warnings...),
	}
	createdProviders := []string{}
	createdCredentials := []string{}
	cleanup := func() {
		for _, id := range createdProviders {
			_ = a.store.DeleteModelProvider(ctx, id)
		}
		for _, id := range createdCredentials {
			_ = a.store.DeleteCredential(ctx, id)
		}
	}

	importedProviderByKey := map[string]*store.ModelProvider{}
	providerKeysSorted := make([]string, 0, len(selectedProviders))
	for key := range selectedProviders {
		providerKeysSorted = append(providerKeysSorted, key)
	}
	sort.Strings(providerKeysSorted)
	for _, key := range providerKeysSorted {
		item := providerByKey[key]
		match := providersByName[strings.ToLower(item.Name)]
		var provider *store.ModelProvider
		if match == nil {
			p, err := a.store.CreateModelProvider(ctx, &store.ModelProvider{
				Name: item.Name, BaseURL: item.BaseURL, WireAPI: item.WireAPI, Enabled: true,
			})
			if err != nil {
				cleanup()
				return nil, err
			}
			createdProviders = append(createdProviders, p.ID)
			provider = p
			result.ImportedProviders++
		} else {
			provider = match
			provider.Name = item.Name
			provider.BaseURL = item.BaseURL
			provider.WireAPI = item.WireAPI
			provider.Enabled = true
			result.UpdatedProviders++
		}
		meta := map[string]any{
			"base_url": provider.BaseURL, "provider_name": provider.Name, "source": "codex-config",
		}
		if item.APIKey != "" {
			if provider.CredentialID == "" {
				cred, err := a.store.CreateCredential(ctx, nil, store.CredentialModelProvider, "model-provider/"+provider.ID, item.APIKey, meta)
				if err != nil {
					cleanup()
					return nil, err
				}
				provider.CredentialID = cred.ID
				createdCredentials = append(createdCredentials, cred.ID)
			} else {
				if _, err := a.store.RotateCredential(ctx, provider.CredentialID, item.APIKey); err != nil {
					cleanup()
					return nil, err
				}
				_ = a.store.UpdateCredentialMetadata(ctx, provider.CredentialID, meta)
			}
		}
		updated, err := a.store.UpdateModelProvider(ctx, provider)
		if err != nil {
			cleanup()
			return nil, err
		}
		importedProviderByKey[key] = updated
		result.Providers = append(result.Providers, *updated)
		if updated.CredentialID == "" {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("供应商 %q 已导入，但还没有可用 API Key，运行前需要补充。", updated.Name))
		}
	}

	profileKeysSorted := make([]string, 0, len(selectedProfiles))
	for key := range selectedProfiles {
		profileKeysSorted = append(profileKeysSorted, key)
	}
	sort.Strings(profileKeysSorted)
	var defaultProfile *store.ModelProfile
	for _, key := range profileKeysSorted {
		item := profileByKey[key]
		provider := importedProviderByKey[item.ProviderKey]
		if provider == nil {
			cleanup()
			return nil, &modelImportSelectionError{message: fmt.Sprintf("Profile %q 依赖的 Provider 未能导入", key)}
		}
		if _, err := a.store.AddProviderModel(ctx, provider.ID, item.Model, true); err != nil {
			cleanup()
			return nil, err
		}
		existing := profilesByName[strings.ToLower(item.Key)]
		profile, err := a.store.UpsertModelProfile(ctx, &store.ModelProfile{
			Name: item.Key, ProviderID: provider.ID, Model: item.Model,
			ReasoningEffort: item.ReasoningEffort, Enabled: true, Source: "codex",
		})
		if err != nil {
			cleanup()
			return nil, err
		}
		if existing == nil {
			result.ImportedProfiles++
		} else {
			result.UpdatedProfiles++
		}
		result.Profiles = append(result.Profiles, *profile)
		if key == parsed.DefaultProfileKey {
			defaultProfile = profile
		}
	}

	if defaultProfile != nil {
		provider := importedProviderByKey[defaultProfileProviderKey(parsed, defaultProfile.Name)]
		if provider != nil && provider.CredentialID != "" {
			for setting, value := range map[string]string{
				"codex_default_provider": provider.ID,
				"codex_default_model":    defaultProfile.Model,
				"codex_default_effort":   defaultProfile.ReasoningEffort,
			} {
				if err := a.store.SetSetting(ctx, setting, value); err != nil {
					cleanup()
					return nil, err
				}
			}
			result.Default = modelDefaultView{
				ProviderID: provider.ID, Model: defaultProfile.Model, ReasoningEffort: defaultProfile.ReasoningEffort,
			}
		} else {
			result.Warnings = append(result.Warnings, "默认 Profile 已导入，但供应商缺少 API Key，未切换系统默认模型。")
		}
	}
	return result, nil
}

func defaultProfileProviderKey(parsed *parsedModelConfig, profileName string) string {
	for _, profile := range parsed.Profiles {
		if profile.Key == profileName {
			return profile.ProviderKey
		}
	}
	return ""
}
