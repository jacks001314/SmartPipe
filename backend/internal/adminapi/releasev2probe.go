package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/store"
)

// settingsCredentialProvider resolves a saved credential by name for the
// probe. It never logs or returns the secret to the browser.
type settingsCredentialProvider struct{ store *store.Store }

func (p settingsCredentialProvider) Resolve(ctx context.Context, ref string) (string, error) {
	_, secret, _, err := p.store.ResolveCredentialByName(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("%w: credential %q", releasepublish.ErrCredentialMissing, ref)
	}
	return secret, nil
}

type probeRequest struct {
	Service string `json:"service"`
}

type probeResult struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
	Detail  string `json:"detail"`
	Admin   bool   `json:"admin,omitempty"`
}

// probeReleaseV2Settings checks a saved external connection without publishing
// anything: the update service is probed through its public key endpoint and a
// read-only admin call, GitHub through GET /user with the saved token.
func (a *API) probeReleaseV2Settings(c iris.Context) {
	ctx := c.Request().Context()
	var in probeRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	switch strings.TrimSpace(in.Service) {
	case "update":
		a.probeUpdateService(ctx, c)
	case "github":
		a.probeGitHub(ctx, c)
	default:
		a.badRequest(c, "service 只支持 update 或 github")
	}
}

func (a *API) probeUpdateService(ctx context.Context, c iris.Context) {
	base, _ := a.store.GetSetting(ctx, "release_update_url")
	credName, _ := a.store.GetSetting(ctx, "release_update_credential")
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		_ = c.JSON(envelope{Data: probeResult{Service: "update", Detail: "还没有保存升级服务地址"}})
		return
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(base + "/v1/pubkey")
	if err != nil {
		_ = c.JSON(envelope{Data: probeResult{Service: "update", Detail: "无法连接 " + base + "：" + err.Error()}})
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = c.JSON(envelope{Data: probeResult{Service: "update", Detail: fmt.Sprintf("%s/v1/pubkey 返回 HTTP %d", base, resp.StatusCode)}})
		return
	}
	if strings.TrimSpace(credName) == "" {
		_ = c.JSON(envelope{Data: probeResult{OK: true, Service: "update", Detail: "服务可达；还没有配置管理令牌，发布前请补上凭据名"}})
		return
	}
	adminClient := &releasepublish.UpdateClient{
		BaseURL: base, Creds: settingsCredentialProvider{store: a.store},
		HTTP: &http.Client{Timeout: 20 * time.Second},
	}
	releases, err := adminClient.ListReleases(ctx, credName, "")
	if err != nil {
		_ = c.JSON(envelope{Data: probeResult{Service: "update", Detail: "服务可达，但管理令牌校验失败：" + err.Error()}})
		return
	}
	_ = c.JSON(envelope{Data: probeResult{OK: true, Service: "update", Admin: true,
		Detail: fmt.Sprintf("服务可达，管理令牌有效（当前 %d 条发布记录）", len(releases))}})
}

func (a *API) probeGitHub(ctx context.Context, c iris.Context) {
	base, _ := a.store.GetSetting(ctx, "release_github_api_base")
	credName, _ := a.store.GetSetting(ctx, "release_github_credential")
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://api.github.com"
	}
	if strings.TrimSpace(credName) == "" {
		_ = c.JSON(envelope{Data: probeResult{Service: "github", Detail: "还没有配置 GitHub 凭据名"}})
		return
	}
	_, secret, _, err := a.store.ResolveCredentialByName(ctx, credName)
	if err != nil || strings.TrimSpace(secret) == "" {
		_ = c.JSON(envelope{Data: probeResult{Service: "github", Detail: "凭据 " + credName + " 不存在或为空"}})
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		_ = c.JSON(envelope{Data: probeResult{Service: "github", Detail: err.Error()}})
		return
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		_ = c.JSON(envelope{Data: probeResult{Service: "github", Detail: "无法连接 " + base + "：" + err.Error()}})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		_ = c.JSON(envelope{Data: probeResult{Service: "github", Detail: fmt.Sprintf("%s/user 返回 HTTP %d：%s", base, resp.StatusCode, summarizeRemote(body))}})
		return
	}
	var user struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(body, &user)
	_ = c.JSON(envelope{Data: probeResult{OK: true, Service: "github", Admin: true,
		Detail: "令牌有效，登录身份：" + firstNonEmpty(user.Login, "unknown")}})
}

func summarizeRemote(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
