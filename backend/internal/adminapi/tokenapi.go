package adminapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

func (a *API) registerAgentTokens(p iris.Party) {
	p.Get("/agent-tokens", a.listAgentTokens)
	p.Post("/agent-tokens", a.createAgentToken)
	p.Post("/agent-tokens/{id}/revoke", a.revokeAgentToken)
	p.Post("/agent-tokens/verify", a.verifyAgentToken)
}

type agentTokenRequest struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	// ExpiresInHours 大于 0 时令牌在若干小时后失效；0 表示长期有效。
	ExpiresInHours int `json:"expires_in_hours"`
}

func (a *API) listAgentTokens(c iris.Context) {
	var projectID *string
	if v := strings.TrimSpace(c.URLParam("project_id")); v != "" {
		projectID = &v
	}
	items, err := a.store.ListAgentTokens(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) createAgentToken(c iris.Context) {
	var in agentTokenRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	switch in.Role {
	case "lead", "dev", "test", "release":
	default:
		a.badRequest(c, "role 必须是 lead/dev/test/release")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		a.badRequest(c, "name 必填")
		return
	}
	var projectID *string
	if strings.TrimSpace(in.ProjectID) != "" {
		project, err := a.store.GetProject(c.Request().Context(), in.ProjectID)
		if err != nil {
			a.fail(c, err)
			return
		}
		v := project.ID
		projectID = &v
	}
	if in.ExpiresInHours < 0 || in.ExpiresInHours > 8760 {
		a.badRequest(c, "expires_in_hours 必须在 0..8760 之间")
		return
	}
	var expiresAt *time.Time
	if in.ExpiresInHours > 0 {
		v := time.Now().UTC().Add(time.Duration(in.ExpiresInHours) * time.Hour)
		expiresAt = &v
	}
	record, token, err := a.store.CreateAgentToken(c.Request().Context(), store.AgentToken{
		ProjectID: projectID, Role: in.Role, Name: in.Name, ExpiresAt: expiresAt,
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "agent_token.create", "agent_token", record.ID, projectID, "ok",
		map[string]any{"role": record.Role, "name": record.Name, "prefix": record.Prefix,
			"expires_at": record.ExpiresAt, "expires_in_hours": in.ExpiresInHours})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"token": token, "record": record}})
}

func (a *API) revokeAgentToken(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	if err := a.store.RevokeAgentToken(c.Request().Context(), id); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "agent_token.revoke", "agent_token", id, nil, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{"id": id, "enabled": false}})
}

func (a *API) verifyAgentToken(c iris.Context) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeBody(c, &in); err != nil || strings.TrimSpace(in.Token) == "" {
		a.badRequest(c, "token 必填")
		return
	}
	t, err := a.store.VerifyAgentToken(c.Request().Context(), in.Token)
	if err != nil {
		c.StatusCode(http.StatusUnauthorized)
		_ = c.JSON(envelope{Err: &apiError{Code: "invalid_token", Message: "令牌无效、已停用或已过期"}})
		return
	}
	_ = c.JSON(envelope{Data: t})
}
