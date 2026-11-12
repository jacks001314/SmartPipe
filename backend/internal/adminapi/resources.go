package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/resourceprobe"
	"smartpipe/backend/internal/store"
)

type resourceWriteRequest struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Username           string   `json:"username"`
	AuthType           string   `json:"auth_type"`
	Secret             string   `json:"secret"`
	Passphrase         string   `json:"passphrase"`
	HostKeyFingerprint string   `json:"host_key_fingerprint"`
	Tags               []string `json:"tags"`
	Enabled            *bool    `json:"enabled"`
}

func (a *API) registerResources(party iris.Party) {
	party.Get("/resources", a.listResources)
	party.Post("/resources", a.createResource)
	party.Get("/resources/available", a.listAvailableResources)
	party.Get("/resources/{id}", a.getResource)
	party.Patch("/resources/{id}", a.updateResource)
	party.Post("/resources/{id}/enabled", a.setResourceEnabled)
	party.Post("/resources/{id}/test", a.testResource)
	party.Post("/resources/{id}/inspect", a.inspectResource)
	party.Delete("/resources/{id}", a.deleteResource)
}

func (a *API) listResources(c iris.Context) {
	items, err := a.store.ListSSHResources(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listAvailableResources(c iris.Context) {
	items, err := a.store.ListAvailableSSHResources(c.Request().Context(), c.URLParam("tag"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) createResource(c iris.Context) {
	var in resourceWriteRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	resource, err := a.store.CreateSSHResource(c.Request().Context(), store.SSHResourceInput{
		Name: in.Name, Description: in.Description, Host: in.Host, Port: in.Port,
		Username: in.Username, AuthType: in.AuthType, Secret: in.Secret,
		Passphrase: in.Passphrase, HostKeyFingerprint: in.HostKeyFingerprint,
		Tags: in.Tags, Enabled: enabled,
	})
	if err != nil {
		a.failResource(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "resource.create", "runtime_resource", resource.ID, nil, "ok",
		map[string]any{"kind": resource.Kind, "name": resource.Name, "host": resource.Host, "port": resource.Port})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: resource})
}

func (a *API) getResource(c iris.Context) {
	resource, err := a.store.GetSSHResource(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: resource})
}

func (a *API) updateResource(c iris.Context) {
	id := c.Params().Get("id")
	var in resourceWriteRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	resource, err := a.store.UpdateSSHResource(c.Request().Context(), id, store.SSHResourceUpdate{
		Name: in.Name, Description: in.Description, Host: in.Host, Port: in.Port,
		Username: in.Username, AuthType: in.AuthType, Secret: in.Secret,
		Passphrase: in.Passphrase, HostKeyFingerprint: in.HostKeyFingerprint,
		Tags: in.Tags, Enabled: enabled,
	})
	if err != nil {
		a.failResource(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "resource.update", "runtime_resource", id, nil, "ok",
		map[string]any{"name": resource.Name, "secret_rotated": strings.TrimSpace(in.Secret) != ""})
	_ = c.JSON(envelope{Data: resource})
}

func (a *API) setResourceEnabled(c iris.Context) {
	id := c.Params().Get("id")
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	resource, err := a.store.SetSSHResourceEnabled(c.Request().Context(), id, in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "resource.enabled", "runtime_resource", id, nil, "ok",
		map[string]any{"enabled": in.Enabled})
	_ = c.JSON(envelope{Data: resource})
}

func (a *API) deleteResource(c iris.Context) {
	id := c.Params().Get("id")
	if err := a.store.DeleteSSHResource(c.Request().Context(), id); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "resource.delete", "runtime_resource", id, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) connectionConfig(c iris.Context) (store.RuntimeResource, resourceprobe.Config, error) {
	resource, secret, passphrase, err := a.store.GetSSHResourceSecret(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		return store.RuntimeResource{}, resourceprobe.Config{}, err
	}
	return *resource, resourceprobe.Config{
		Host: resource.Host, Port: resource.Port, Username: resource.Username,
		AuthType: resource.AuthType, Secret: secret, Passphrase: passphrase,
		PinnedFingerprint: resource.HostKeyFingerprint,
	}, nil
}

func (a *API) testResource(c iris.Context) {
	ctx := c.Request().Context()
	resource, cfg, err := a.connectionConfig(c)
	if err != nil {
		a.fail(c, err)
		return
	}
	result := a.resourceProber.Test(ctx, cfg)
	updated, updateErr := a.store.RecordSSHResourceCheck(ctx, resource.ID, result.OK, result.Message, result.Fingerprint)
	if updateErr != nil {
		a.fail(c, updateErr)
		return
	}
	auditResult := "failed"
	if result.OK {
		auditResult = "ok"
	}
	a.audit(ctx, c, "resource.test", "runtime_resource", resource.ID, nil, auditResult,
		map[string]any{"host": resource.Host, "port": resource.Port, "fingerprint": result.Fingerprint})
	_ = c.JSON(envelope{Data: map[string]any{
		"ok": result.OK, "message": result.Message, "latency_ms": result.LatencyMS,
		"fingerprint": result.Fingerprint, "server_version": result.ServerVersion,
		"resource": updated,
	}})
}

func (a *API) inspectResource(c iris.Context) {
	ctx := c.Request().Context()
	resource, cfg, err := a.connectionConfig(c)
	if err != nil {
		a.fail(c, err)
		return
	}
	inspection := a.resourceProber.Inspect(ctx, cfg)
	updated, updateErr := a.store.RecordSSHResourceInspection(ctx, resource.ID, inspection.OK, inspection.Message, inspection.Fingerprint, inspection.SystemInfo)
	if updateErr != nil {
		a.fail(c, updateErr)
		return
	}
	auditResult := "failed"
	if inspection.OK {
		auditResult = "ok"
	}
	a.audit(ctx, c, "resource.inspect", "runtime_resource", resource.ID, nil, auditResult,
		map[string]any{"host": resource.Host, "port": resource.Port, "fingerprint": inspection.Fingerprint})
	_ = c.JSON(envelope{Data: map[string]any{
		"ok": inspection.OK, "message": inspection.Message, "latency_ms": inspection.LatencyMS,
		"fingerprint": inspection.Fingerprint, "server_version": inspection.ServerVersion,
		"system_info": inspection.SystemInfo, "resource": updated,
	}})
}

func (a *API) failResource(c iris.Context, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		c.StatusCode(http.StatusConflict)
		_ = c.JSON(envelope{Err: &apiError{Code: "conflict", Message: "同名资源已存在，请换一个名称"}})
	case errors.Is(err, store.ErrNotFound):
		a.fail(c, err)
	case errors.Is(err, context.DeadlineExceeded):
		a.badRequest(c, "操作超时，请稍后重试")
	default:
		a.badRequest(c, err.Error())
	}
}
