package adminapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

func (a *API) listCredentials(c iris.Context) {
	var projectID *string
	if v := strings.TrimSpace(c.URLParam("project_id")); v != "" {
		projectID = &v
	}
	items, err := a.store.ListCredentials(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

type createCredentialRequest struct {
	ProjectID *string        `json:"project_id"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Value     string         `json:"value"`
	Metadata  map[string]any `json:"metadata"`
}

func (a *API) createCredential(c iris.Context) {
	var in createCredentialRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	// 全局凭据允许不传 project_id，空字符串也按全局处理（否则会去查一个不存在的项目）。
	if in.ProjectID != nil && strings.TrimSpace(*in.ProjectID) == "" {
		in.ProjectID = nil
	}
	if !store.ValidKind(in.Kind) {
		a.badRequest(c, "不支持的凭据类型: "+in.Kind)
		return
	}
	cred, err := a.store.CreateCredential(c.Request().Context(), in.ProjectID, in.Kind, in.Name, in.Value, in.Metadata)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "credential.create", "credential", cred.ID, in.ProjectID, "ok",
		map[string]any{"kind": cred.Kind, "name": cred.Name})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: cred})
}

func (a *API) rotateCredential(c iris.Context) {
	id := c.Params().Get("id")
	var in struct {
		Value string `json:"value"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	cred, err := a.store.RotateCredential(c.Request().Context(), id, in.Value)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "credential.rotate", "credential", cred.ID, cred.ProjectID, "ok",
		map[string]any{"kind": cred.Kind})
	_ = c.JSON(envelope{Data: cred})
}

func (a *API) setCredentialEnabled(c iris.Context) {
	id := c.Params().Get("id")
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	cred, err := a.store.SetCredentialEnabled(c.Request().Context(), id, in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "credential.enabled", "credential", cred.ID, cred.ProjectID, "ok",
		map[string]any{"enabled": in.Enabled})
	_ = c.JSON(envelope{Data: cred})
}

func (a *API) deleteCredential(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	if err := a.store.DeleteCredential(ctx, id); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "credential.delete", "credential", id, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) testCredential(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	kind, secret, meta, err := a.store.GetSecret(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	res := a.checker.Dispatch(ctx, kind, secret, meta)
	_ = a.store.RecordCredentialTest(ctx, id, res.OK, res.Message)
	result := "ok"
	if !res.OK {
		result = "failed"
	}
	a.audit(ctx, c, "credential.test", "credential", id, nil, result,
		map[string]any{"kind": kind, "ok": res.OK})

	items, _ := a.store.ListCredentials(ctx, nil)
	var updated *store.Credential
	for i := range items {
		if items[i].ID == id {
			updated = &items[i]
			break
		}
	}
	_ = c.JSON(envelope{Data: iris.Map{
		"ok":         res.OK,
		"message":    res.Message,
		"latency_ms": res.Latency,
		"credential": updated,
	}})
}

func (a *API) listAudit(c iris.Context) {
	limit := 100
	if v := c.URLParam("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	prefix := c.URLParam("action")
	items, err := a.store.ListAudit(c.Request().Context(), limit, prefix)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}
