package adminapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/store"
)

func (a *API) registerBridge(p iris.Party) {
	p = p.Party("/bridge")
	p.Get("/links", a.listBridgeLinks)
	p.Get("/deliveries", a.listWebhookDeliveries)
	p.Get("/dispatches", a.listDispatches)
	p.Get("/plans", a.listPlans)
	p.Post("/links/{id}/redispatch", a.redispatch)
	p.Get("/bindings", a.listBindings)
	p.Put("/bindings/{projectId}", a.upsertBinding)
	p.Delete("/bindings/{projectId}", a.deleteBinding)
}

func (a *API) listBridgeLinks(c iris.Context) {
	limit := intQuery(c, "limit", 100)
	items, err := a.store.ListBridgeLinks(c.Request().Context(), limit)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listWebhookDeliveries(c iris.Context) {
	limit := intQuery(c, "limit", 50)
	items, err := a.store.ListWebhookDeliveries(c.Request().Context(), limit)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listDispatches(c iris.Context) {
	limit := intQuery(c, "limit", 50)
	items, err := a.store.ListDispatches(c.Request().Context(), limit)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listPlans(c iris.Context) {
	items, err := a.store.ListRecentPlans(c.Request().Context(), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listBindings(c iris.Context) {
	items, err := a.store.ListBindings(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

type bindingRequest struct {
	AppServerID    string `json:"app_server_id"`
	AppServerAddr  string `json:"app_server_addr"`
	AgentProjectID string `json:"agent_project_id"`
	LeadAgentID    string `json:"lead_agent_id"`
	LeadAgentName  string `json:"lead_agent_name"`
}

func (a *API) upsertBinding(c iris.Context) {
	projectID := strings.TrimSpace(c.Params().Get("projectId"))
	if projectID == "" {
		a.badRequest(c, "缺少 projectId")
		return
	}
	if _, err := a.store.GetProject(c.Request().Context(), projectID); err != nil {
		a.fail(c, err)
		return
	}
	var in bindingRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.AppServerID) == "" || strings.TrimSpace(in.LeadAgentID) == "" {
		a.badRequest(c, "app_server_id 与 lead_agent_id 必填")
		return
	}
	addr := strings.TrimSpace(in.AppServerAddr)
	if addr == "" {
		if inst, ok := a.runtime.Get(in.AppServerID); ok {
			addr = inst.Addr
		}
	}
	agentProject := strings.TrimSpace(in.AgentProjectID)
	if agentProject == "" {
		// 向 App Server 确认 lead 的真实项目归属；投递消息必须用它。
		if client, err := a.runtime.Client(in.AppServerID); err == nil {
			if agents, listErr := client.AgentList(c.Request().Context()); listErr == nil {
				for _, agent := range agents {
					if id, _ := agent["id"].(string); id != strings.TrimSpace(in.LeadAgentID) {
						continue
					}
					if spec, ok := agent["spec"].(map[string]any); ok {
						agentProject, _ = spec["project_id"].(string)
					}
					if name, _ := agent["name"].(string); strings.TrimSpace(in.LeadAgentName) == "" {
						in.LeadAgentName = name
					}
				}
			}
		}
	}
	if agentProject == "" {
		agentProject = projectID
	}
	b, err := a.store.UpsertBinding(c.Request().Context(), store.ProjectBinding{
		ProjectID:      projectID,
		AppServerID:    strings.TrimSpace(in.AppServerID),
		AppServerAddr:  addr,
		AgentProjectID: agentProject,
		LeadAgentID:    strings.TrimSpace(in.LeadAgentID),
		LeadAgentName:  strings.TrimSpace(in.LeadAgentName),
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "bridge.binding.upsert", "project", projectID, &projectID, "ok",
		map[string]any{"app_server_id": b.AppServerID, "lead_agent_id": b.LeadAgentID, "agent_project_id": b.AgentProjectID})
	_ = c.JSON(envelope{Data: b})
}

func (a *API) deleteBinding(c iris.Context) {
	projectID := strings.TrimSpace(c.Params().Get("projectId"))
	if err := a.store.DeleteBinding(c.Request().Context(), projectID); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "bridge.binding.delete", "project", projectID, &projectID, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) redispatch(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.badRequest(c, "非法 link id")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
	defer cancel()
	link, err := a.store.GetBridgeLink(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	if link.ProjectID == nil {
		a.badRequest(c, "该消息没有关联项目，无法派单")
		return
	}
	project, err := a.store.GetProject(ctx, *link.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	d := bridge.NewLeadDispatcher(a.store)
	d.Token = a.appServerToken
	agentID, dispErr := d.Dispatch(ctx, link, project, "")
	status := "sent"
	if dispErr != nil {
		status = "failed"
		_ = a.store.MarkDispatched(ctx, link.ID, agentID, status, dispErr.Error())
	} else {
		_ = a.store.MarkDispatched(ctx, link.ID, agentID, status, "")
	}
	updated, _ := a.store.GetBridgeLink(ctx, link.ID)
	a.audit(ctx, c, "bridge.redispatch", "issue_link", strconv.FormatInt(link.ID, 10), link.ProjectID, status,
		map[string]any{"agent_id": agentID, "issue": link.IssueURL})
	if dispErr != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Data: updated, Err: &apiError{Code: "dispatch_failed", Message: dispErr.Error()}})
		return
	}
	_ = c.JSON(envelope{Data: updated})
}

func intQuery(c iris.Context, key string, fallback int) int {
	if v := c.URLParam(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
