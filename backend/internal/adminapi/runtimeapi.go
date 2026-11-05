package adminapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	rt "smartpipe/backend/internal/runtime"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/syntropyclient"
)

type startAppServerRequest struct {
	Name      string   `json:"name"`
	Addr      string   `json:"addr"`
	Binary    string   `json:"binary"`
	Token     string   `json:"token"`
	DataDir   string   `json:"data_dir"`
	NodeDir   string   `json:"node_dir"`
	LogDir    string   `json:"log_dir"`
	ExtraArgs []string `json:"extra_args"`
}

func (a *API) registerRuntime(p iris.Party) {
	p = p.Party("/runtime")
	p.Get("/app-servers", a.listAppServers)
	p.Post("/app-servers", a.startAppServer)
	p.Get("/app-servers/{id}", a.getAppServer)
	p.Delete("/app-servers/{id}", a.removeAppServer)
	p.Post("/app-servers/{id}/stop", a.stopAppServer)
	p.Post("/app-servers/{id}/restart", a.restartAppServer)
	p.Get("/app-servers/{id}/logs", a.appServerLogs)
	p.Get("/app-servers/{id}/health", a.appServerHealth)

	p.Get("/app-servers/{id}/master/status", a.masterStatus)
	p.Get("/app-servers/{id}/master/registry", a.masterRegistry)
	p.Post("/app-servers/{id}/master/deploy", a.masterDeploy)
	p.Delete("/app-servers/{id}/master", a.masterDelete)
	p.Get("/app-servers/{id}/nodes", a.nodeList)
	p.Post("/app-servers/{id}/nodes", a.nodeCreate)
	p.Get("/app-servers/{id}/agent-templates", a.templateList)
	p.Post("/app-servers/{id}/agent-templates", a.templateCreate)
	p.Get("/app-servers/{id}/agents", a.agentList)
	p.Post("/app-servers/{id}/agents", a.deployAgent)
	p.Get("/app-servers/{id}/agent-operations/{op}", a.agentOperation)
}

func (a *API) listAppServers(c iris.Context) {
	items := a.runtime.List()
	ctx, cancel := context.WithTimeout(c.Request().Context(), 6*time.Second)
	defer cancel()
	for i := range items {
		if refreshed, err := a.runtime.Health(ctx, items[i].ID); err == nil {
			items[i] = refreshed
		}
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) startAppServer(c iris.Context) {
	var in startAppServerRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	binary := firstNonEmpty(in.Binary, a.appServerBinary)
	if binary == "" {
		a.badRequest(c, "未配置 App Server 二进制路径（SMARTPIPE_APPSERVER_BIN）")
		return
	}
	addr := firstNonEmpty(in.Addr, "127.0.0.1:8765")
	dataDir := firstNonEmpty(in.DataDir, a.runtimeDataDir)
	if dataDir == "" {
		a.badRequest(c, "未配置运行时数据目录（SMARTPIPE_RUNTIME_DATA）")
		return
	}
	dirs := rt.Dirs{
		NodeDir:       firstNonEmpty(in.NodeDir, filepath.Join(dataDir, "nodes")),
		MasterDataDir: filepath.Join(dataDir, "master"),
		CredentialDir: filepath.Join(dataDir, "cred"),
		LogDir:        firstNonEmpty(in.LogDir, filepath.Join(dataDir, "logs")),
		MasterRunRoot: filepath.Join(dataDir, "master-run"),
	}
	spec := rt.Spec{
		Name:   firstNonEmpty(in.Name, "app-01"),
		Binary: binary,
		Addr:   addr,
		Token:  firstNonEmpty(in.Token, a.appServerToken),
		Dirs:   dirs,
		Args:   in.ExtraArgs,
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), 60*time.Second)
	defer cancel()
	inst, err := a.runtime.Start(ctx, spec)
	if err != nil {
		a.audit(c.Request().Context(), c, "runtime.app_server.start", "app_server", spec.Name, nil, "failed",
			map[string]any{"error": err.Error(), "addr": addr})
		c.StatusCode(http.StatusBadRequest)
		_ = c.JSON(envelope{Err: &apiError{Code: "start_failed", Message: err.Error()}})
		return
	}
	a.audit(c.Request().Context(), c, "runtime.app_server.start", "app_server", inst.ID, nil, "ok",
		map[string]any{"addr": inst.Addr, "pid": inst.PID})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: inst})
}

func (a *API) getAppServer(c iris.Context) {
	id := c.Params().Get("id")
	inst, ok := a.runtime.Get(id)
	if !ok {
		a.fail(c, rt.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer cancel()
	if refreshed, err := a.runtime.Health(ctx, id); err == nil {
		inst = refreshed
	}
	_ = c.JSON(envelope{Data: inst})
}

func (a *API) removeAppServer(c iris.Context) {
	id := c.Params().Get("id")
	if err := a.runtime.Remove(id); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.app_server.remove", "app_server", id, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) stopAppServer(c iris.Context) {
	id := c.Params().Get("id")
	if err := a.runtime.Stop(id, 8*time.Second); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.app_server.stop", "app_server", id, nil, "ok", nil)
	inst, _ := a.runtime.Get(id)
	_ = c.JSON(envelope{Data: inst})
}

func (a *API) restartAppServer(c iris.Context) {
	id := c.Params().Get("id")
	ctx, cancel := context.WithTimeout(c.Request().Context(), 60*time.Second)
	defer cancel()
	inst, err := a.runtime.Restart(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.app_server.restart", "app_server", id, nil, "ok",
		map[string]any{"pid": inst.PID})
	_ = c.JSON(envelope{Data: inst})
}

func (a *API) appServerHealth(c iris.Context) {
	id := c.Params().Get("id")
	ctx, cancel := context.WithTimeout(c.Request().Context(), 8*time.Second)
	defer cancel()
	inst, err := a.runtime.Health(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: inst})
}

func (a *API) appServerLogs(c iris.Context) {
	id := c.Params().Get("id")
	lines := 200
	if v := c.URLParam("lines"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	logs, err := a.runtime.LogTail(id, lines)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"id": id, "lines": lines, "log": logs}})
}

func (a *API) clientFor(c iris.Context) (*syntropyclient.Client, string, bool) {
	id := c.Params().Get("id")
	client, err := a.runtime.Client(id)
	if err != nil {
		a.fail(c, err)
		return nil, id, false
	}
	return client, id, true
}

func (a *API) masterStatus(c iris.Context) {
	client, _, ok := a.clientFor(c)
	if !ok {
		return
	}
	out, err := client.MasterStatus(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) masterRegistry(c iris.Context) {
	client, _, ok := a.clientFor(c)
	if !ok {
		return
	}
	out, err := client.MasterRegistry(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) masterDeploy(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	var in syntropyclient.MasterDeployRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if in.Target == "" {
		in.Target = "local"
	}
	out, err := client.DeployMaster(c.Request().Context(), in)
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.master.deploy", "app_server", id, nil, "ok",
		map[string]any{"target": in.Target})
	_ = c.JSON(envelope{Data: out})
}

func (a *API) masterDelete(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	if err := client.DeleteMaster(c.Request().Context(), c.URLParam("masterId")); err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.master.delete", "app_server", id, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) nodeList(c iris.Context) {
	client, _, ok := a.clientFor(c)
	if !ok {
		return
	}
	out, err := client.NodeList(c.Request().Context(), c.URLParam("masterId"), 1, 50)
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) nodeCreate(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	var in syntropyclient.Raw
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	out, err := client.NodeCreate(c.Request().Context(), in)
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.node.create", "app_server", id, nil, "ok",
		map[string]any{"name": in["name"]})
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: out})
}

func (a *API) templateList(c iris.Context) {
	client, _, ok := a.clientFor(c)
	if !ok {
		return
	}
	items, err := client.TemplateList(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) templateCreate(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	var in syntropyclient.Raw
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	name, _ := in["template_name"].(string)
	if strings.TrimSpace(name) == "" {
		a.badRequest(c, "template_name 不能为空")
		return
	}
	out, err := client.TemplateCreate(c.Request().Context(), in)
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.template.create", "app_server", id, nil, "ok",
		map[string]any{"template": name})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: out})
}

func (a *API) agentList(c iris.Context) {
	client, _, ok := a.clientFor(c)
	if !ok {
		return
	}
	agents, err := client.AgentList(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: agents})
}

type deployAgentRequest struct {
	ProjectID    string            `json:"project_id"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name"`
	TemplateName string            `json:"template_name"`
	TargetNodeID string            `json:"target_node_id"`
	Labels       map[string]string `json:"labels"`
	// BindLead 为 true 时，部署成功后把该 Agent 绑定为项目的 pipeline-lead。
	BindLead bool `json:"bind_lead"`
}

func (a *API) deployAgent(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	var in deployAgentRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		a.badRequest(c, "Agent 名称不能为空")
		return
	}
	spec := syntropyclient.AgentSpec{
		ProjectID:    firstNonEmpty(in.ProjectID, "default"),
		Name:         in.Name,
		DisplayName:  in.DisplayName,
		TargetNodeID: in.TargetNodeID,
		TemplateName: firstNonEmpty(in.TemplateName, "pipeline-lead-v3"),
		Labels:       in.Labels,
	}
	out, err := client.DeployAgent(c.Request().Context(), spec, c.GetHeader("X-Request-Id"))
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.agent.deploy", "agent", in.Name, stringPtr(in.ProjectID), "ok",
		map[string]any{"app_server_id": id, "template": spec.TemplateName})
	if in.BindLead && strings.TrimSpace(in.ProjectID) != "" {
		if agentID := firstMapString(out, "agentId", "agent_id"); strings.TrimSpace(agentID) != "" {
			inst, _ := a.runtime.Get(id)
			if _, bindErr := a.store.UpsertBinding(c.Request().Context(), store.ProjectBinding{
				ProjectID:      strings.TrimSpace(in.ProjectID),
				AppServerID:    id,
				AppServerAddr:  inst.Addr,
				AgentProjectID: strings.TrimSpace(in.ProjectID),
				LeadAgentID:    agentID,
				LeadAgentName:  in.Name,
			}); bindErr != nil {
				a.audit(c.Request().Context(), c, "bridge.binding.upsert", "project", in.ProjectID, stringPtr(in.ProjectID), "failed",
					map[string]any{"error": bindErr.Error()})
			} else {
				a.audit(c.Request().Context(), c, "bridge.binding.upsert", "project", in.ProjectID, stringPtr(in.ProjectID), "ok",
					map[string]any{"lead_agent_id": agentID})
			}
		}
	}
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: out})
}

func (a *API) agentOperation(c iris.Context) {
	client, _, ok := a.clientFor(c)
	if !ok {
		return
	}
	out, err := client.AgentOperation(c.Request().Context(), c.Params().Get("op"))
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) upstreamFail(c iris.Context, err error) {
	var se *syntropyclient.StatusError
	status := http.StatusBadGateway
	code := "upstream_error"
	switch {
	case errors.Is(err, syntropyclient.ErrUnauthorized):
		status = http.StatusUnauthorized
		code = "app_server_unauthorized"
	case errors.As(err, &se):
		status = http.StatusBadGateway
		code = "app_server_status"
	}
	c.StatusCode(status)
	_ = c.JSON(envelope{Err: &apiError{Code: code, Message: err.Error()}})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func stringPtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		var empty *string
		return empty
	}
	return &v
}
