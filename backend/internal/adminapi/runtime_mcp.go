package adminapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/syntropyclient"
)

// registerRuntimeMCP 提供 App Server 全局 MCP Pool 的管理接口。
func (a *API) registerRuntimeMCP(p iris.Party) {
	p.Get("/app-servers/{id}/mcp", a.getMCPPool)
	p.Post("/app-servers/{id}/mcp/servers", a.upsertMCPServer)
	p.Delete("/app-servers/{id}/mcp/servers/{name}", a.deleteMCPServer)
	p.Get("/app-servers/{id}/mcp/statuses", a.mcpStatuses)
	p.Get("/mcp-config", a.mcpConfig)
	p.Put("/mcp-config", a.updateMCPConfig)
}

// poolFrom 从 /api/settings 响应中取出 mcpPool.servers。
func poolFrom(settings syntropyclient.Raw) map[string]any {
	root, _ := settings["settings"].(map[string]any)
	if root == nil {
		root, _ = settings["data"].(map[string]any)
	}
	if root == nil {
		return map[string]any{}
	}
	pool, _ := root["mcpPool"].(map[string]any)
	if pool == nil {
		pool, _ = root["mcp_pool"].(map[string]any)
	}
	servers, _ := pool["servers"].(map[string]any)
	if servers == nil {
		return map[string]any{}
	}
	return servers
}

// summarizePool 隐藏 headers 中的字面值，只保留结构信息。
func summarizePool(servers map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(servers))
	for name, raw := range servers {
		entry := map[string]any{"name": name}
		if server, ok := raw.(map[string]any); ok {
			if httpCfg, ok := server["http"].(map[string]any); ok {
				entry["transport"] = "http"
				entry["url"], _ = httpCfg["url"].(string)
				entry["bearer_token_env_var"], _ = httpCfg["bearerTokenEnvVar"].(string)
				if headers, ok := httpCfg["headers"].(map[string]any); ok {
					names := make([]string, 0, len(headers))
					for k := range headers {
						names = append(names, k)
					}
					entry["header_names"] = names
				}
			} else if stdioCfg, ok := server["stdio"].(map[string]any); ok {
				entry["transport"] = "stdio"
				entry["command"], _ = stdioCfg["command"].(string)
			} else if sdkCfg, ok := server["sdk"].(map[string]any); ok {
				entry["transport"] = "sdk"
				entry["name"], _ = sdkCfg["name"].(string)
			}
		}
		out = append(out, entry)
	}
	return out
}

func (a *API) getMCPPool(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	settings, err := client.GetSettings(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"app_server_id": id, "servers": summarizePool(poolFrom(settings))}})
}

type mcpServerRequest struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	TokenEnv string `json:"token_env_var"`
}

// UpsertMCPServer 把（或更新）一个 HTTP MCP Server 写入全局 MCP Pool。
func (a *API) upsertMCPServer(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	var in mcpServerRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	if in.Name == "" || in.URL == "" {
		a.badRequest(c, "name 与 url 必填")
		return
	}
	if in.TokenEnv == "" {
		in.TokenEnv = "SMARTPIPE_MCP_TOKEN"
	}
	if err := ensureMCPServer(c.Request().Context(), client, in.Name, in.URL, in.TokenEnv); err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.mcp.upsert", "app_server", id, nil, "ok",
		map[string]any{"server": in.Name, "url": in.URL, "token_env": in.TokenEnv})
	_ = c.JSON(envelope{Data: iris.Map{"app_server_id": id, "server": in.Name, "url": in.URL, "token_env_var": in.TokenEnv}})
}

func (a *API) deleteMCPServer(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Params().Get("name"))
	if name == "" {
		a.badRequest(c, "缺少 server 名称")
		return
	}
	settings, err := client.GetSettings(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	servers := poolFrom(settings)
	if _, exists := servers[name]; !exists {
		c.StatusCode(http.StatusNotFound)
		_ = c.JSON(envelope{Err: &apiError{Code: "not_found", Message: "MCP Server 不存在"}})
		return
	}
	delete(servers, name)
	if _, err := client.UpdateSettings(c.Request().Context(), syntropyclient.Raw{
		"mcpPool": map[string]any{"servers": servers},
	}); err != nil {
		a.upstreamFail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "runtime.mcp.delete", "app_server", id, nil, "ok", map[string]any{"server": name})
	_ = c.JSON(envelope{Data: iris.Map{"deleted": name}})
}

func (a *API) mcpStatuses(c iris.Context) {
	client, id, ok := a.clientFor(c)
	if !ok {
		return
	}
	out, err := client.MCPAuthStatuses(c.Request().Context())
	if err != nil {
		a.upstreamFail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"app_server_id": id, "statuses": out}})
}

// ensureMCPServer 写入（或更新）一个 HTTP MCP Server，保留池内其它条目。
func ensureMCPServer(ctx context.Context, client *syntropyclient.Client, name, urlStr, tokenEnv string) error {
	if strings.TrimSpace(tokenEnv) == "" {
		tokenEnv = "SMARTPIPE_MCP_TOKEN"
	}
	settings, err := client.GetSettings(ctx)
	if err != nil {
		return err
	}
	servers := poolFrom(settings)
	servers[name] = map[string]any{"http": map[string]any{
		"url": urlStr, "bearerTokenEnvVar": tokenEnv,
	}}
	_, err = client.UpdateSettings(ctx, syntropyclient.Raw{
		"mcpPool": map[string]any{"servers": servers},
	})
	return err
}
