package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/store"
)

const (
	githubManagedAppCredentialName = "github-app-managed"
	githubAppSetupTTL              = 30 * time.Minute
)

// GitHub App Manifest 流程用于把“创建应用、取得私钥、安装到仓库”收进产品入口。
// 浏览器只承担 GitHub 必需的登录与授权确认；应用凭据始终由后端接收和加密保存。
type githubAppSetup struct {
	baseURL        string
	appName        string
	status         string
	message        string
	appID          int64
	slug           string
	owner          string
	pem            string
	installationID string
	credential     string
	expiresAt      time.Time
}

type githubAppSetupState struct {
	mu    sync.Mutex
	flows map[string]*githubAppSetup
}

func newGitHubAppSetupState() *githubAppSetupState {
	return &githubAppSetupState{flows: map[string]*githubAppSetup{}}
}

type githubAppSetupView struct {
	SetupID  string `json:"setup_id"`
	Launch   string `json:"launch_url"`
	Expires  int    `json:"expires_in"`
	Interval int    `json:"poll_interval"`
}

type githubAppStatusView struct {
	Status       string `json:"status"`
	AppSlug      string `json:"app_slug,omitempty"`
	Owner        string `json:"owner,omitempty"`
	Installation string `json:"installation_id,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
	Message      string `json:"message,omitempty"`
}

type manifestConversion struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	PEM          string `json:"pem"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Owner        struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (a *API) registerGitHubAppPublic(pub iris.Party) {
	pub.Get("/github/app/launch/{id}", a.launchGitHubApp)
	pub.Get("/github/app/callback", a.githubAppManifestCallback)
	pub.Get("/github/app/install-callback/{id}", a.githubAppInstallCallback)
}

func (a *API) registerGitHubApp(party iris.Party) {
	party.Post("/github/app/start", a.startGitHubApp)
	party.Get("/github/app/status/{id}", a.githubAppStatus)
}

func (a *API) startGitHubApp(c iris.Context) {
	ctx := c.Request().Context()
	baseURL := requestBaseURL(c)
	id := newFlowID()
	now := time.Now()
	name := "SmartPipe-" + strings.ToUpper(id[:6])
	setup := &githubAppSetup{
		baseURL: baseURL, appName: name, status: "pending", expiresAt: now.Add(githubAppSetupTTL),
	}
	a.appSetups.mu.Lock()
	for key, item := range a.appSetups.flows {
		if now.After(item.expiresAt) {
			delete(a.appSetups.flows, key)
		}
	}
	a.appSetups.flows[id] = setup
	a.appSetups.mu.Unlock()
	a.audit(ctx, c, "github.app.start", "setting", id, nil, "ok", map[string]any{"name": name, "base_url": baseURL})
	_ = c.JSON(envelope{Data: githubAppSetupView{
		SetupID: id, Launch: strings.TrimRight(baseURL, "/") + "/admin/github/app/launch/" + id,
		Expires: int(githubAppSetupTTL.Seconds()), Interval: 2,
	}})
}

func (a *API) githubAppStatus(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	a.appSetups.mu.Lock()
	setup, ok := a.appSetups.flows[id]
	if !ok {
		a.appSetups.mu.Unlock()
		c.StatusCode(http.StatusNotFound)
		_ = c.JSON(envelope{Err: &apiError{Code: "not_found", Message: "GitHub 连接流程不存在或已清理"}})
		return
	}
	view := githubAppStatusView{
		Status: setup.status, AppSlug: setup.slug, Owner: setup.owner,
		Installation: setup.installationID, CredentialID: setup.credential, Message: setup.message,
	}
	if time.Now().After(setup.expiresAt) && setup.status != "installed" {
		view.Status = "expired"
	}
	a.appSetups.mu.Unlock()
	_ = c.JSON(envelope{Data: view})
}

func (a *API) launchGitHubApp(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	a.appSetups.mu.Lock()
	setup, ok := a.appSetups.flows[id]
	if !ok || time.Now().After(setup.expiresAt) {
		a.appSetups.mu.Unlock()
		writeHTML(c, http.StatusGone, "<!doctype html><meta charset=utf-8><h2>连接已过期</h2><p>请回到 SmartPipe 重新点击“连接 GitHub”。</p>")
		return
	}
	baseURL, name := setup.baseURL, setup.appName
	a.appSetups.mu.Unlock()

	manifest := map[string]any{
		"name":         name,
		"url":          strings.TrimRight(baseURL, "/"),
		"description":  "SmartPipe GitHub App，用于 Issue、评论、代码检查与发版流程。",
		"redirect_url": strings.TrimRight(baseURL, "/") + "/admin/github/app/callback",
		"callback_urls": []string{
			strings.TrimRight(baseURL, "/") + "/admin/github/app/callback",
		},
		"setup_url":       strings.TrimRight(baseURL, "/") + "/admin/github/app/install-callback/" + id,
		"setup_on_update": true,
		"public":          false,
		"default_permissions": map[string]string{
			"contents": "write", "issues": "write", "metadata": "read", "pull_requests": "write",
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		writeHTML(c, http.StatusInternalServerError, "<!doctype html><meta charset=utf-8><h2>生成 GitHub 应用配置失败</h2>")
		return
	}
	page := fmt.Sprintf(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>连接 GitHub</title>
<body style="font-family:system-ui,sans-serif;padding:32px;color:#182230">
<h2>正在打开 GitHub</h2>
<p>请在 GitHub 登录并确认创建 SmartPipe GitHub App。</p>
<form id="manifest-form" method="post" action="https://github.com/settings/apps/new?state=%s">
<input type="hidden" name="manifest" value="%s">
<noscript><button type="submit">继续前往 GitHub</button></noscript>
</form>
<script>document.getElementById('manifest-form').submit()</script>
</body>`, url.QueryEscape(id), html.EscapeString(string(raw)))
	writeHTML(c, http.StatusOK, page)
}

func (a *API) githubAppManifestCallback(c iris.Context) {
	ctx := c.Request().Context()
	state := strings.TrimSpace(c.URLParam("state"))
	code := strings.TrimSpace(c.URLParam("code"))
	if state == "" || code == "" {
		writeHTML(c, http.StatusBadRequest, "<!doctype html><meta charset=utf-8><h2>GitHub 返回参数不完整</h2>")
		return
	}
	a.appSetups.mu.Lock()
	setup, ok := a.appSetups.flows[state]
	if !ok || time.Now().After(setup.expiresAt) {
		a.appSetups.mu.Unlock()
		writeHTML(c, http.StatusGone, "<!doctype html><meta charset=utf-8><h2>连接已过期</h2><p>请回到 SmartPipe 重新连接。</p>")
		return
	}
	a.appSetups.mu.Unlock()

	conv, err := exchangeGitHubAppManifest(ctx, code)
	if err != nil {
		a.updateGitHubAppSetup(state, func(s *githubAppSetup) { s.status = "error"; s.message = err.Error() })
		writeHTML(c, http.StatusBadGateway, "<!doctype html><meta charset=utf-8><h2>GitHub App 创建失败</h2><p>请回到 SmartPipe 重试。</p>")
		return
	}
	if conv.ID == 0 || strings.TrimSpace(conv.Slug) == "" || strings.TrimSpace(conv.PEM) == "" {
		a.updateGitHubAppSetup(state, func(s *githubAppSetup) { s.status = "error"; s.message = "GitHub 未返回完整的应用凭据" })
		writeHTML(c, http.StatusBadGateway, "<!doctype html><meta charset=utf-8><h2>GitHub App 凭据不完整</h2>")
		return
	}
	a.updateGitHubAppSetup(state, func(s *githubAppSetup) {
		s.status = "created"
		s.appID = conv.ID
		s.slug = conv.Slug
		s.owner = conv.Owner.Login
		s.pem = conv.PEM
		s.message = "应用已创建，正在进入仓库安装"
	})
	a.audit(ctx, c, "github.app.created", "setting", state, nil, "ok", map[string]any{"app_id": conv.ID, "slug": conv.Slug})
	installURL := "https://github.com/apps/" + url.PathEscape(conv.Slug) + "/installations/new"
	page := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<body style="font-family:system-ui,sans-serif;padding:32px;color:#182230">
<h2>SmartPipe GitHub App 已创建</h2>
<p>即将进入仓库安装页。完成后会自动回到 SmartPipe，无需复制任何 token。</p>
<p><a href="%s">继续安装</a></p>
<script>
if (window.opener) window.opener.postMessage({type:'smartpipe-github-app',status:'created'}, '*');
window.location.replace(%q);
</script>
</body>`, html.EscapeString(installURL), installURL)
	writeHTML(c, http.StatusOK, page)
}

func (a *API) githubAppInstallCallback(c iris.Context) {
	ctx := c.Request().Context()
	id := strings.TrimSpace(c.Params().Get("id"))
	installationID := strings.TrimSpace(c.URLParam("installation_id"))
	a.appSetups.mu.Lock()
	setup, ok := a.appSetups.flows[id]
	if !ok || time.Now().After(setup.expiresAt) {
		a.appSetups.mu.Unlock()
		writeHTML(c, http.StatusGone, "<!doctype html><meta charset=utf-8><h2>连接已过期</h2><p>请回到 SmartPipe 重新连接。</p>")
		return
	}
	if setup.pem == "" || setup.appID == 0 {
		a.appSetups.mu.Unlock()
		writeHTML(c, http.StatusBadRequest, "<!doctype html><meta charset=utf-8><h2>GitHub App 尚未创建完成</h2>")
		return
	}
	appID, pemValue, slug, owner := setup.appID, setup.pem, setup.slug, setup.owner
	a.appSetups.mu.Unlock()
	if installationID == "" {
		a.updateGitHubAppSetup(id, func(s *githubAppSetup) { s.status = "error"; s.message = "GitHub 未返回 installation_id" })
		writeHTML(c, http.StatusBadRequest, "<!doctype html><meta charset=utf-8><h2>GitHub 未返回安装编号</h2>")
		return
	}
	meta := map[string]any{
		"app_id": strconv.FormatInt(appID, 10), "installation_id": installationID,
		"slug": slug, "owner": owner, "source": "app_manifest",
		"connected_at": time.Now().UTC().Format(time.RFC3339),
	}
	cred, err := a.upsertManagedGitHubApp(ctx, pemValue, meta)
	if err != nil {
		a.updateGitHubAppSetup(id, func(s *githubAppSetup) { s.status = "error"; s.message = "保存 GitHub App 凭据失败" })
		writeHTML(c, http.StatusInternalServerError, "<!doctype html><meta charset=utf-8><h2>保存 GitHub 凭据失败</h2>")
		return
	}
	a.updateGitHubAppSetup(id, func(s *githubAppSetup) {
		s.status = "installed"
		s.installationID = installationID
		s.credential = cred.ID
		s.pem = ""
		s.message = "GitHub App 已安装，安装 token 由后台自动换取"
	})
	a.audit(ctx, c, "github.app.installed", "credential", cred.ID, nil, "ok", map[string]any{"app_id": appID, "slug": slug, "installation_id": installationID})
	writeHTML(c, http.StatusOK, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<body style="font-family:system-ui,sans-serif;padding:32px;color:#182230">
<h2>GitHub 已连接</h2>
<p>应用已安装，凭据已加密保存。这个窗口可以关闭，SmartPipe 会自动继续。</p>
<script>
if (window.opener) window.opener.postMessage({type:'smartpipe-github-app',status:'installed'}, '*');
setTimeout(()=>window.close(), 700);
</script>
</body>`)
}

func exchangeGitHubAppManifest(ctx context.Context, code string) (*manifestConversion, error) {
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := githubOAuthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub App manifest conversion returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out manifestConversion
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("GitHub App manifest response is not json: %w", err)
	}
	return &out, nil
}

func (a *API) upsertManagedGitHubApp(ctx context.Context, pemValue string, meta map[string]any) (*store.Credential, error) {
	defer bridge.ResetCredentialCache()
	items, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		return nil, err
	}
	for i := range items {
		item := &items[i]
		if item.Kind != store.CredentialGitHubApp {
			continue
		}
		if source, _ := item.Metadata["source"].(string); source == "app_manifest" {
			if _, err := a.store.RotateCredential(ctx, item.ID, pemValue); err != nil {
				return nil, err
			}
			if err := a.store.UpdateCredentialMetadata(ctx, item.ID, meta); err != nil {
				return nil, err
			}
			if !item.Enabled {
				return a.store.SetCredentialEnabled(ctx, item.ID, true)
			}
			item.Metadata = meta
			return item, nil
		}
	}
	return a.store.CreateCredential(ctx, nil, store.CredentialGitHubApp, githubManagedAppCredentialName, pemValue, meta)
}

func (a *API) updateGitHubAppSetup(id string, fn func(*githubAppSetup)) {
	a.appSetups.mu.Lock()
	defer a.appSetups.mu.Unlock()
	if setup, ok := a.appSetups.flows[id]; ok {
		fn(setup)
	}
}

func requestBaseURL(c iris.Context) string {
	scheme := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if scheme == "" {
		if c.Request().TLS != nil || strings.EqualFold(c.Request().URL.Scheme, "https") {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = c.Host()
	}
	return scheme + "://" + host
}

func writeHTML(c iris.Context, status int, body string) {
	c.StatusCode(status)
	c.ContentType("text/html; charset=utf-8")
	_, _ = c.WriteString(body)
}
