package adminapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

func (a *API) registerIdentities(p iris.Party) {
	p.Get("/identities", a.listIdentities)
	p.Post("/identities", a.upsertIdentity)
	p.Delete("/identities/{id}", a.deleteIdentity)
}

func (a *API) listIdentities(c iris.Context) {
	var projectID *string
	if v := strings.TrimSpace(c.URLParam("project_id")); v != "" {
		projectID = &v
	}
	items, err := a.store.ListUserIdentities(c.Request().Context(), c.URLParam("platform"), projectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) upsertIdentity(c iris.Context) {
	var in struct {
		Platform       string  `json:"platform"`
		PlatformUserID string  `json:"platform_user_id"`
		ProjectID      *string `json:"project_id"`
		GitHubLogin    string  `json:"github_login"`
		DisplayName    string  `json:"display_name"`
		Enabled        *bool   `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Platform) == "" {
		in.Platform = "lanxin"
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	projectID := in.ProjectID
	if projectID != nil && strings.TrimSpace(*projectID) == "" {
		projectID = nil
	}
	item, err := a.store.UpsertUserIdentity(c.Request().Context(), store.UserIdentity{
		Platform: in.Platform, PlatformUserID: in.PlatformUserID, ProjectID: projectID,
		GitHubLogin: in.GitHubLogin, DisplayName: in.DisplayName, Enabled: enabled,
	})
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	a.audit(c.Request().Context(), c, "identity.upsert", "identity", strconv.FormatInt(item.ID, 10), projectID, "ok",
		map[string]any{"platform": item.Platform, "user": item.PlatformUserID, "github_login": item.GitHubLogin})
	_ = c.JSON(envelope{Data: item})
}

func (a *API) deleteIdentity(c iris.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Params().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		a.badRequest(c, "非法 ID")
		return
	}
	if err := a.store.DeleteUserIdentity(c.Request().Context(), id); err != nil {
		if err == store.ErrNotFound {
			c.StatusCode(http.StatusNotFound)
			_ = c.JSON(envelope{Err: &apiError{Code: "not_found", Message: "映射不存在"}})
			return
		}
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "identity.delete", "identity", strconv.FormatInt(id, 10), nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}
