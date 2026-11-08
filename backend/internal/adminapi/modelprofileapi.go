package adminapi

import (
	"net/http"

	"github.com/kataras/iris/v12"
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
