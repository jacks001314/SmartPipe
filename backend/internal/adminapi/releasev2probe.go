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
	// Target 是可选的升级服务名；为空时探测全部已配置的服务。
	Target string `json:"target"`
}

type probeResult struct {
	OK      bool                `json:"ok"`
	Service string              `json:"service"`
	Detail  string              `json:"detail"`
	Admin   bool                `json:"admin,omitempty"`
	Targets []probeTargetResult `json:"targets,omitempty"`
}

type probeTargetResult struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	OK     bool   `json:"ok"`
	Admin  bool   `json:"admin,omitempty"`
	Detail string `json:"detail"`
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
		a.probeUpdateService(ctx, c, strings.TrimSpace(in.Target))
	case "github":
		a.probeGitHub(ctx, c)
	default:
		a.badRequest(c, "service 只支持 update 或 github")
	}
}

func (a *API) probeUpdateService(ctx context.Context, c iris.Context, only string) {
	services := a.updateServices(ctx)
	if len(services) == 0 {
		_ = c.JSON(envelope{Data: probeResult{Service: "update", Detail: "还没有保存升级服务地址"}})
		return
	}
	client := &http.Client{Timeout: 20 * time.Second}
	adminClient := &http.Client{Timeout: 20 * time.Second}
	results := make([]probeTargetResult, 0, len(services))
	allOK := true
	matched := false
	for _, service := range services {
		if only != "" && service.Name != only {
			continue
		}
		matched = true
		entry := probeTargetResult{Name: service.Name, URL: service.URL}
		resp, err := client.Get(strings.TrimRight(service.URL, "/") + "/v1/pubkey")
		if err != nil {
			entry.Detail = "无法连接：" + err.Error()
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				entry.Detail = fmt.Sprintf("/v1/pubkey 返回 HTTP %d", resp.StatusCode)
			} else if strings.TrimSpace(service.Credential) == "" {
				entry.OK = true
				entry.Detail = "服务可达；还没有填写凭据名和管理令牌"
			} else if _, secret, _, cerr := a.store.ResolveCredentialByName(ctx, service.Credential); cerr != nil || strings.TrimSpace(secret) == "" {
				entry.OK = true
				entry.Detail = fmt.Sprintf("服务可达；凭据 %s 还没有保存管理令牌，填写后保存再测试", service.Credential)
			} else {
				update := &releasepublish.UpdateClient{
					BaseURL: service.URL, Creds: settingsCredentialProvider{store: a.store}, HTTP: adminClient,
				}
				releases, lerr := update.ListReleases(ctx, service.Credential, "")
				if lerr != nil {
					entry.Detail = "服务可达，但管理令牌校验失败：" + lerr.Error()
				} else {
					entry.OK = true
					entry.Admin = true
					entry.Detail = fmt.Sprintf("服务可达，管理令牌有效（%d 条发布记录）", len(releases))
				}
			}
		}
		if !entry.OK {
			allOK = false
		}
		results = append(results, entry)
	}
	if only != "" && !matched {
		_ = c.JSON(envelope{Data: probeResult{Service: "update", Detail: "没有名为 " + only + " 的升级服务"}})
		return
	}
	okCount := 0
	for _, entry := range results {
		if entry.OK {
			okCount++
		}
	}
	out := probeResult{
		OK: allOK, Service: "update", Targets: results,
		Detail: fmt.Sprintf("%d/%d 个升级服务可用", okCount, len(results)),
	}
	if len(results) == 1 {
		out.Admin = results[0].Admin
		out.Detail = results[0].Detail
	}
	_ = c.JSON(envelope{Data: out})
}

// updateServices 读取界面上维护的升级服务列表；空列表时回落到旧的单服务字段。
func (a *API) updateServices(ctx context.Context) []releaseUpdateService {
	if raw, err := a.store.GetSetting(ctx, "release_update_services"); err == nil && strings.TrimSpace(raw) != "" {
		var services []releaseUpdateService
		if json.Unmarshal([]byte(raw), &services) == nil && len(services) > 0 {
			return services
		}
	}
	base, _ := a.store.GetSetting(ctx, "release_update_url")
	cred, _ := a.store.GetSetting(ctx, "release_update_credential")
	if strings.TrimSpace(base) == "" {
		return nil
	}
	return []releaseUpdateService{{Name: "默认", URL: strings.TrimRight(strings.TrimSpace(base), "/"), Credential: strings.TrimSpace(cred), Enabled: true}}
}

func (a *API) probeGitHub(ctx context.Context, c iris.Context) {
	base, _ := a.store.GetSetting(ctx, "release_github_api_base")
	credName, _ := a.store.GetSetting(ctx, "release_github_credential")
	if strings.TrimSpace(credName) == "" {
		// 默认复用「GitHub 配置」页保存的全局凭据（PAT 或 OAuth）。
		if items, err := a.store.ListCredentials(ctx, nil); err == nil {
			for _, item := range items {
				if item.Kind == "github_pat" && item.ProjectID == nil && item.Enabled {
					credName = item.Name
					break
				}
			}
		}
	}
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
