package adminapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/github"
)

const githubProxySettingKey = "github_proxy"

type githubProxyView struct {
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

func (a *API) registerGitHubProxy(party iris.Party) {
	party.Get("/github/proxy", a.getGitHubProxy)
	party.Put("/github/proxy", a.putGitHubProxy)
	party.Post("/github/proxy/test", a.testGitHubProxy)
}

func (a *API) githubProxyURL(ctx context.Context) string {
	if v, err := a.store.GetSetting(ctx, githubProxySettingKey); err == nil {
		return strings.TrimSpace(v)
	}
	return ""
}

// currentGitHubClient 返回当前生效的 GitHub HTTP 客户端。
func (a *API) currentGitHubClient() *http.Client {
	a.githubHTTPMu.RLock()
	defer a.githubHTTPMu.RUnlock()
	if a.githubHTTP != nil {
		return a.githubHTTP
	}
	return github.HTTPClient()
}

// applyGitHubProxy 按代理地址重建 GitHub 客户端，并同步给 OAuth 与 go-github 调用。
func (a *API) applyGitHubProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if raw == "" {
		transport.Proxy = http.ProxyFromEnvironment
	} else {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("代理地址不合法，示例：http://172.17.0.1:7890")
		}
		switch parsed.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("代理只支持 http、https、socks5")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	a.githubHTTPMu.Lock()
	a.githubHTTP = client
	a.githubHTTPMu.Unlock()
	github.SetHTTPClient(client)
	githubOAuthHTTP = client
	return nil
}

func (a *API) applyStoredGitHubProxy(ctx context.Context) {
	_ = a.applyGitHubProxy(a.githubProxyURL(ctx))
}

func (a *API) getGitHubProxy(c iris.Context) {
	ctx := c.Request().Context()
	current := a.githubProxyURL(ctx)
	view := githubProxyView{URL: current, Enabled: current != "", Source: "settings"}
	if current == "" {
		view.Source = "direct"
	}
	_ = c.JSON(envelope{Data: view})
}

func (a *API) putGitHubProxy(c iris.Context) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	ctx := c.Request().Context()
	if err := a.applyGitHubProxy(in.URL); err != nil {
		a.badRequest(c, err.Error())
		return
	}
	if err := a.store.SetSetting(ctx, githubProxySettingKey, strings.TrimSpace(in.URL)); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "github.proxy.update", "setting", githubProxySettingKey, nil, "ok",
		map[string]any{"configured": strings.TrimSpace(in.URL) != ""})
	_ = c.JSON(envelope{Data: githubProxyView{
		URL: strings.TrimSpace(in.URL), Enabled: strings.TrimSpace(in.URL) != "", Source: "settings",
	}})
}

func (a *API) testGitHubProxy(c iris.Context) {
	var in struct {
		URL string `json:"url"`
	}
	_ = decodeBody(c, &in)
	proxy := strings.TrimSpace(in.URL)
	if proxy == "" {
		if v, err := a.store.GetSetting(c.Request().Context(), githubProxySettingKey); err == nil {
			proxy = strings.TrimSpace(v)
		}
	}
	client := a.currentGitHubClient()
	if strings.TrimSpace(in.URL) != "" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Host == "" {
			a.badRequest(c, "代理地址不合法")
			return
		}
		transport.Proxy = http.ProxyURL(parsed)
		client = &http.Client{Timeout: 20 * time.Second, Transport: transport}
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/rate_limit", nil)
	if err != nil {
		a.fail(c, err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "smartpipe/"+a.version)
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		_ = c.JSON(envelope{Data: iris.Map{"ok": false, "message": "连接失败：" + err.Error(), "latency_ms": latency}})
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	message := "GitHub API 可达"
	if !ok {
		message = fmt.Sprintf("GitHub 返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	_ = c.JSON(envelope{Data: iris.Map{"ok": ok, "message": message, "latency_ms": latency}})
}
