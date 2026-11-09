package adminapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

func (a *API) listModelProfiles(c iris.Context) {
	items, err := a.store.ListModelProfiles(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) setModelProfileEnabled(c iris.Context) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	profile, err := a.store.SetModelProfileEnabled(c.Request().Context(), c.Params().Get("id"), in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "model.profile.enabled", "model_profile", profile.ID, nil, "ok",
		map[string]any{"enabled": in.Enabled})
	_ = c.JSON(envelope{Data: profile})
}

func (a *API) deleteModelProfile(c iris.Context) {
	ctx := c.Request().Context()
	id := c.Params().Get("id")
	if err := a.store.DeleteModelProfile(ctx, id); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "model.profile.delete", "model_profile", id, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) setModelProfileDefault(c iris.Context) {
	ctx := c.Request().Context()
	profile, err := a.store.GetModelProfile(ctx, c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	if !profile.Enabled {
		a.badRequest(c, "Profile 已停用")
		return
	}
	provider, err := a.store.GetModelProvider(ctx, profile.ProviderID)
	if err != nil {
		a.fail(c, err)
		return
	}
	if !provider.Enabled || provider.CredentialID == "" {
		a.badRequest(c, "Profile 依赖的供应商未启用或缺少 API Key")
		return
	}
	for key, value := range map[string]string{
		"codex_default_provider": profile.ProviderID,
		"codex_default_model":    profile.Model,
		"codex_default_effort":   profile.ReasoningEffort,
	} {
		if err := a.store.SetSetting(ctx, key, value); err != nil {
			a.fail(c, err)
			return
		}
	}
	a.audit(ctx, c, "model.profile.default", "model_profile", profile.ID, nil, "ok",
		map[string]any{"provider_id": profile.ProviderID, "model": profile.Model, "effort": profile.ReasoningEffort})
	_ = c.JSON(envelope{Data: modelDefaultView{
		ProviderID: profile.ProviderID, Model: profile.Model, ReasoningEffort: profile.ReasoningEffort,
	}})
}

// ensureModelProfile 在供应商模型被启用后自动生成对应档案，
// 让项目创建页无需再导入配置就能选到这个模型。
func (a *API) ensureModelProfile(ctx context.Context, c iris.Context, providerID, modelID string) error {
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if p.ProviderID != providerID || !strings.EqualFold(p.Model, modelID) {
			continue
		}
		if p.Enabled {
			return nil
		}
		if _, err := a.store.SetModelProfileEnabled(ctx, p.ID, true); err != nil {
			return err
		}
		a.audit(ctx, c, "model.profile.auto_enable", "model_profile", p.ID, nil, "ok",
			map[string]any{"model": modelID, "trigger": "catalog_enabled"})
		return nil
	}
	provider, err := a.store.GetModelProvider(ctx, providerID)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(provider.Name)
	if name == "" {
		name = providerID
	}
	if profileNameTaken(profiles, name) {
		name = name + " · " + modelID
	}
	if profileNameTaken(profiles, name) {
		// 重名时跳过建档，避免 Upsert 覆盖同名的其它档案。
		return nil
	}
	profile, err := a.store.UpsertModelProfile(ctx, &store.ModelProfile{
		Name: name, ProviderID: providerID, Model: modelID, Enabled: true, Source: "auto",
	})
	if err != nil {
		return err
	}
	a.audit(ctx, c, "model.profile.auto_create", "model_profile", profile.ID, nil, "ok",
		map[string]any{"model": modelID, "provider_id": providerID})
	return nil
}

// setCatalogModelProfilesEnabled 让档案跟随目录模型的启停，避免档案指向已停用的模型。
func (a *API) setCatalogModelProfilesEnabled(ctx context.Context, c iris.Context, providerID, modelID string, enabled bool) error {
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if p.ProviderID != providerID || !strings.EqualFold(p.Model, modelID) || p.Enabled == enabled {
			continue
		}
		if _, err := a.store.SetModelProfileEnabled(ctx, p.ID, enabled); err != nil {
			return err
		}
		a.audit(ctx, c, "model.profile.auto_toggle", "model_profile", p.ID, nil, "ok",
			map[string]any{"model": modelID, "enabled": enabled})
	}
	return nil
}

// purgeModelProfiles 删除目录模型时清理对应档案，避免留下选不中的空档案。
func (a *API) purgeModelProfiles(ctx context.Context, c iris.Context, providerID, modelID string) {
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		return
	}
	for _, p := range profiles {
		if p.ProviderID != providerID || !strings.EqualFold(p.Model, modelID) {
			continue
		}
		if err := a.store.DeleteModelProfile(ctx, p.ID); err != nil {
			continue
		}
		a.audit(ctx, c, "model.profile.auto_delete", "model_profile", p.ID, nil, "ok",
			map[string]any{"model": modelID})
	}
}

func profileNameTaken(profiles []store.ModelProfile, name string) bool {
	for _, p := range profiles {
		if strings.EqualFold(strings.TrimSpace(p.Name), name) {
			return true
		}
	}
	return false
}
