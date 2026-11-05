package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/gate"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/syntropyclient"
)

var errNoAppServer = errors.New("adminapi: no app server bound for project")

type derivationRequest struct {
	ProjectID    string `json:"project_id"`
	IssueRef     string `json:"issue_ref"`
	AgentName    string `json:"agent_name"`
	DisplayName  string `json:"display_name"`
	TemplateName string `json:"template_name"`
	TargetNodeID string `json:"target_node_id"`
	AppServerID  string `json:"app_server_id"`
}

func (a *API) registerDerivations(p iris.Party) {
	p.Get("/derivations", a.listDerivations)
	p.Post("/derivations", a.createDerivation)
}

func (a *API) listDerivations(c iris.Context) {
	items, err := a.store.ListDerivations(c.Request().Context(), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

// createDerivation 是 lead 派生子 Agent 的唯一入口：先过批准门禁，再调 App Server。
func (a *API) createDerivation(c iris.Context) {
	var in derivationRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.ProjectID) == "" || strings.TrimSpace(in.IssueRef) == "" || strings.TrimSpace(in.AgentName) == "" {
		a.badRequest(c, "project_id、issue_ref、agent_name 必填")
		return
	}
	ctx := c.Request().Context()
	project, err := a.store.GetProject(ctx, in.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	state, err := gate.Check(ctx, a.store, project, in.IssueRef)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	if !state.OK {
		a.audit(ctx, c, "agent.derive", "issue", in.IssueRef, &project.ID, "blocked",
			map[string]any{"reason": state.Reason, "version": state.Version})
		c.StatusCode(http.StatusForbidden)
		_ = c.JSON(envelope{Err: &apiError{Code: "approval_required", Message: state.Reason}})
		return
	}

	client, addr, err := a.appServerClient(ctx, in.ProjectID, in.AppServerID)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	spec := syntropyclient.AgentSpec{
		ProjectID:    project.ID,
		Name:         strings.TrimSpace(in.AgentName),
		DisplayName:  in.DisplayName,
		TargetNodeID: in.TargetNodeID,
		TemplateName: firstNonEmpty(in.TemplateName, project.LeadAgentTemplate),
		Labels:       map[string]string{"issue_ref": in.IssueRef},
	}
	out, err := client.DeployAgent(ctx, spec, c.GetHeader("X-Request-Id"))
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	agentID := firstMapString(out, "agentId", "agent_id")
	operationID := firstMapString(out, "id")
	record, err := a.store.CreateDerivation(ctx, store.Derivation{
		ProjectID: &project.ID, IssueRef: in.IssueRef, AgentName: spec.Name,
		AgentID: agentID, OperationID: operationID,
		ApprovalVersion: state.Version, ApprovalHash: state.PlanHash,
		Status: "submitted",
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "agent.derive", "agent", spec.Name, &project.ID, "ok",
		map[string]any{"issue": in.IssueRef, "agent_id": agentID, "operation_id": operationID, "approval_version": state.Version})
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: iris.Map{"derivation": record, "operation": out, "approval": state, "app_server_addr": addr}})
}

// appServerClient 优先用显式 App Server，其次用项目绑定。
func (a *API) appServerClient(ctx context.Context, projectID, appServerID string) (*syntropyclient.Client, string, error) {
	if strings.TrimSpace(appServerID) != "" {
		client, err := a.runtime.Client(appServerID)
		if err != nil {
			return nil, "", err
		}
		inst, _ := a.runtime.Get(appServerID)
		return client, inst.Addr, nil
	}
	binding, err := a.store.GetBinding(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(binding.AppServerID) != "" {
		if client, err := a.runtime.Client(binding.AppServerID); err == nil {
			return client, binding.AppServerAddr, nil
		}
	}
	if strings.TrimSpace(binding.AppServerAddr) == "" {
		return nil, "", errNoAppServer
	}
	return syntropyclient.New(binding.AppServerAddr, a.appServerToken), binding.AppServerAddr, nil
}

// firstMapString 兼容 protojson 的 camelCase 与 snake_case 两种键。
func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
