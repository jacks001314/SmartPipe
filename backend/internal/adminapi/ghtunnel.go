package adminapi

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

// GitHub Webhook 公网入口的内嵌 frp 隧道配置。
//
// 这些参数由 bridge 进程读取后启动 frpc（以库方式），配置页改完 30 秒内生效。
const (
	githubTunnelEnabledKey    = "github_tunnel_enabled"
	githubTunnelServerKey     = "github_tunnel_server"
	githubTunnelServerPortKey = "github_tunnel_server_port"
	githubTunnelRemotePortKey = "github_tunnel_remote_port"
	githubTunnelProxyNameKey  = "github_tunnel_proxy_name"
)

type githubTunnelView struct {
	Enabled    bool   `json:"enabled"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	RemotePort int    `json:"remote_port"`
	ProxyName  string `json:"proxy_name"`
	TokenSet   bool   `json:"token_set"`
	Source     string `json:"source"`
}

func (a *API) registerGitHubTunnel(party iris.Party) {
	party.Get("/github/tunnel", a.getGitHubTunnel)
	party.Put("/github/tunnel", a.putGitHubTunnel)
}

func (a *API) githubTunnelView(ctx context.Context) githubTunnelView {
	view := githubTunnelView{
		ServerPort: 7000,
		RemotePort: 18090,
		ProxyName:  "smartpipe-webhook",
		Source:     "settings",
	}
	if v, err := a.store.GetSetting(ctx, githubTunnelEnabledKey); err == nil {
		view.Enabled = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, err := a.store.GetSetting(ctx, githubTunnelServerKey); err == nil {
		view.Server = strings.TrimSpace(v)
	}
	if v, err := a.store.GetSetting(ctx, githubTunnelServerPortKey); err == nil {
		if port, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil && port > 0 {
			view.ServerPort = port
		}
	}
	if v, err := a.store.GetSetting(ctx, githubTunnelRemotePortKey); err == nil {
		if port, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil && port > 0 {
			view.RemotePort = port
		}
	}
	if v, err := a.store.GetSetting(ctx, githubTunnelProxyNameKey); err == nil && strings.TrimSpace(v) != "" {
		view.ProxyName = strings.TrimSpace(v)
	}
	view.TokenSet = a.findFRPTokenCredential(ctx) != nil || strings.TrimSpace(os.Getenv("SMARTPIPE_FRP_TOKEN")) != ""
	return view
}

func (a *API) findFRPTokenCredential(ctx context.Context) *store.Credential {
	items, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		return nil
	}
	for i := range items {
		if items[i].Enabled && items[i].Kind == store.CredentialFRPToken {
			return &items[i]
		}
	}
	return nil
}

func (a *API) getGitHubTunnel(c iris.Context) {
	_ = c.JSON(envelope{Data: a.githubTunnelView(c.Request().Context())})
}

func (a *API) putGitHubTunnel(c iris.Context) {
	var in struct {
		Enabled    bool   `json:"enabled"`
		Server     string `json:"server"`
		ServerPort int    `json:"server_port"`
		RemotePort int    `json:"remote_port"`
		ProxyName  string `json:"proxy_name"`
		Token      string `json:"token"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	ctx := c.Request().Context()
	server := strings.TrimSpace(in.Server)
	if in.Enabled && server == "" {
		a.badRequest(c, "启用隧道时必须填写 frps 地址")
		return
	}
	if in.ServerPort <= 0 || in.ServerPort > 65535 {
		a.badRequest(c, "frps 端口不合法")
		return
	}
	if in.RemotePort <= 0 || in.RemotePort > 65535 {
		a.badRequest(c, "远端映射端口不合法")
		return
	}
	proxyName := strings.TrimSpace(in.ProxyName)
	if proxyName == "" {
		proxyName = "smartpipe-webhook"
	}

	settings := map[string]string{
		githubTunnelEnabledKey:    strconv.FormatBool(in.Enabled),
		githubTunnelServerKey:     server,
		githubTunnelServerPortKey: strconv.Itoa(in.ServerPort),
		githubTunnelRemotePortKey: strconv.Itoa(in.RemotePort),
		githubTunnelProxyNameKey:  proxyName,
	}
	for key, value := range settings {
		if err := a.store.SetSetting(ctx, key, value); err != nil {
			a.fail(c, err)
			return
		}
	}

	if token := strings.TrimSpace(in.Token); token != "" {
		existing := a.findFRPTokenCredential(ctx)
		if existing != nil {
			if _, err := a.store.RotateCredential(ctx, existing.ID, token); err != nil {
				a.fail(c, err)
				return
			}
		} else if _, err := a.store.CreateCredential(ctx, nil, store.CredentialFRPToken, "frps token", token, nil); err != nil {
			a.fail(c, err)
			return
		}
	}

	a.audit(ctx, c, "github.tunnel.update", "setting", githubTunnelEnabledKey, nil, "ok",
		map[string]any{"enabled": in.Enabled, "server": server, "remote_port": in.RemotePort})
	_ = c.JSON(envelope{Data: a.githubTunnelView(ctx)})
}
