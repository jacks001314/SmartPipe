package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

func (a *API) registerToolCalls(p iris.Party) {
	p.Get("/tool-calls", a.listToolCalls)
	p.Post("/tool-calls", a.createToolCall)
}

func (a *API) listToolCalls(c iris.Context) {
	items, err := a.store.ListToolCalls(c.Request().Context(), strings.TrimSpace(c.URLParam("tool")), intQuery(c, "limit", 100))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

type toolCallRequest struct {
	ProjectID   *string         `json:"project_id"`
	TokenID     string          `json:"token_id"`
	AgentName   string          `json:"agent_name"`
	Role        string          `json:"role"`
	Tool        string          `json:"tool"`
	Status      string          `json:"status"`
	DurationMS  int             `json:"duration_ms"`
	ArgsSummary json.RawMessage `json:"args_summary"`
	Error       string          `json:"error"`
}

func (a *API) createToolCall(c iris.Context) {
	var in toolCallRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Tool) == "" || strings.TrimSpace(in.TokenID) == "" {
		a.badRequest(c, "tool 与 token_id 必填")
		return
	}
	if strings.TrimSpace(in.Status) == "" {
		in.Status = "ok"
	}
	rec, err := a.store.CreateToolCall(c.Request().Context(), store.ToolCall{
		ProjectID: in.ProjectID, TokenID: in.TokenID, AgentName: in.AgentName, Role: in.Role,
		Tool: in.Tool, Status: in.Status, DurationMS: in.DurationMS,
		ArgsSummary: in.ArgsSummary, Error: truncateText(in.Error, 2000),
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: rec})
}

// ApplyRuntimeSettings 让数据库里的 MCP 配置覆盖环境变量（管理台可编辑后持久生效）。
func (a *API) ApplyRuntimeSettings(ctx context.Context) {
	if v, err := a.store.GetSetting(ctx, "mcp_url"); err == nil && strings.TrimSpace(v) != "" {
		a.mcpURL = strings.TrimSpace(v)
	}
	if v, err := a.store.GetSetting(ctx, "mcp_pool_name"); err == nil && strings.TrimSpace(v) != "" {
		a.mcpPoolName = strings.TrimSpace(v)
	}
	a.applyStoredGitHubProxy(ctx)
}

func (a *API) mcpConfig(c iris.Context) {
	_ = c.JSON(envelope{Data: iris.Map{"url": a.mcpURL, "pool_name": a.mcpPoolName}})
}

func (a *API) updateMCPConfig(c iris.Context) {
	var in struct {
		URL      string `json:"url"`
		PoolName string `json:"pool_name"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	in.PoolName = strings.TrimSpace(in.PoolName)
	if in.URL == "" || !strings.HasPrefix(in.URL, "http") {
		a.badRequest(c, "url 必须是 http(s) 地址")
		return
	}
	if in.PoolName == "" {
		in.PoolName = "smartpipe"
	}
	if err := a.store.SetSetting(c.Request().Context(), "mcp_url", in.URL); err != nil {
		a.fail(c, err)
		return
	}
	if err := a.store.SetSetting(c.Request().Context(), "mcp_pool_name", in.PoolName); err != nil {
		a.fail(c, err)
		return
	}
	a.mcpURL, a.mcpPoolName = in.URL, in.PoolName
	a.audit(c.Request().Context(), c, "runtime.mcp.config", "settings", "mcp", nil, "ok",
		map[string]any{"url": in.URL, "pool_name": in.PoolName})
	_ = c.JSON(envelope{Data: iris.Map{"url": a.mcpURL, "pool_name": a.mcpPoolName}})
}

var _ = http.StatusOK
