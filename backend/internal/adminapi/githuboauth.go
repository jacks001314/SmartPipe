package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

// GitHub OAuth 设备码流程。客户端（控制台）只拿到 flow_id 与 user_code，
// device_code 与 access_token 始终留在服务端。
var (
	githubOAuthBase = "https://github.com"
	githubAPIBase   = "https://api.github.com"
	githubOAuthHTTP = &http.Client{Timeout: 15 * time.Second}
)

const (
	githubOAuthCredentialName = "github-oauth"
	githubOAuthClientKey      = "github_oauth_client_id"
	githubOAuthScopeKey       = "github_oauth_scope"
	// GitHub CLI 的 OAuth App 是公开客户端，并已启用 Device Flow。把它作为
	// SmartPipe 的内置登录入口，用户只需要登录 GitHub 并批准，不需要填写
	// Client ID。部署方可随时通过 PUT /admin/github/oauth 改用自建 OAuth App。
	githubOAuthDefaultClientID = "Iv1.b507a08c87ecfe98"
)

type githubOAuthFlow struct {
	clientID   string
	deviceCode string
	interval   time.Duration
	expiresAt  time.Time
	lastPoll   time.Time
}

type githubOAuthState struct {
	mu    sync.Mutex
	flows map[string]*githubOAuthFlow
}

func newGitHubOAuthState() *githubOAuthState {
	return &githubOAuthState{flows: map[string]*githubOAuthFlow{}}
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type accessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Interval         int    `json:"interval"`
}

func postForm(ctx context.Context, endpoint string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := githubOAuthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github oauth returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github oauth response is not json: %w", err)
	}
	return nil
}

func requestDeviceCode(ctx context.Context, clientID, scope string) (*deviceCodeResponse, error) {
	var out deviceCodeResponse
	err := postForm(ctx, strings.TrimRight(githubOAuthBase, "/")+"/login/device/code", url.Values{
		"client_id": {clientID},
		"scope":     {scope},
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return nil, fmt.Errorf("github oauth returned empty device code")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 900
	}
	return &out, nil
}

func pollAccessToken(ctx context.Context, clientID, deviceCode string) (*accessTokenResponse, error) {
	var out accessTokenResponse
	err := postForm(ctx, strings.TrimRight(githubOAuthBase, "/")+"/login/oauth/access_token", url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func fetchGitHubLogin(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(githubAPIBase, "/")+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubOAuthHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github user api returned %d", resp.StatusCode)
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Login == "" {
		return "", fmt.Errorf("github user api returned empty login")
	}
	return out.Login, nil
}

func newFlowID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (a *API) oauthSettings(ctx context.Context) (clientID, scope string) {
	clientID, _ = a.store.GetSetting(ctx, githubOAuthClientKey)
	scope, _ = a.store.GetSetting(ctx, githubOAuthScopeKey)
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = githubOAuthDefaultClientID
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		// 默认申请 repo：模拟“账号能看到的仓库”，包含别人私有仓库里的协作邀请。
		scope = "repo"
	}
	return clientID, scope
}

func (a *API) findOAuthCredential(ctx context.Context) *store.Credential {
	items, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		return nil
	}
	for i := range items {
		c := &items[i]
		if c.Kind != store.CredentialGitHubPAT {
			continue
		}
		if src, _ := c.Metadata["source"].(string); src == "oauth_device" {
			return c
		}
	}
	return nil
}

type githubOAuthView struct {
	Configured   bool   `json:"configured"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope"`
	Authorized   bool   `json:"authorized"`
	Login        string `json:"login"`
	CredentialID string `json:"credential_id"`
}

func (a *API) githubOAuthView(ctx context.Context) githubOAuthView {
	clientID, scope := a.oauthSettings(ctx)
	view := githubOAuthView{Configured: clientID != "", ClientID: clientID, Scope: scope}
	if cred := a.findOAuthCredential(ctx); cred != nil && cred.Enabled {
		view.Authorized = true
		view.CredentialID = cred.ID
		view.Login, _ = cred.Metadata["login"].(string)
		view.Scope, _ = cred.Metadata["scope"].(string)
	}
	return view
}

func (a *API) registerGitHubOAuth(party iris.Party) {
	party.Get("/github/oauth", a.getGitHubOAuth)
	party.Put("/github/oauth", a.putGitHubOAuth)
	party.Post("/github/oauth/device", a.startGitHubOAuth)
	party.Post("/github/oauth/poll", a.pollGitHubOAuth)
	party.Delete("/github/oauth", a.deleteGitHubOAuth)
}

func (a *API) getGitHubOAuth(c iris.Context) {
	_ = c.JSON(envelope{Data: a.githubOAuthView(c.Request().Context())})
}

func (a *API) putGitHubOAuth(c iris.Context) {
	var in struct {
		ClientID string `json:"client_id"`
		Scope    string `json:"scope"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" {
		a.badRequest(c, "Client ID 不能为空")
		return
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = "public_repo"
	}
	if scope != "public_repo" && scope != "repo" {
		a.badRequest(c, "scope 只支持 public_repo 或 repo")
		return
	}
	ctx := c.Request().Context()
	if err := a.store.SetSetting(ctx, githubOAuthClientKey, clientID); err != nil {
		a.fail(c, err)
		return
	}
	if err := a.store.SetSetting(ctx, githubOAuthScopeKey, scope); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "github.oauth.config", "setting", githubOAuthClientKey, nil, "ok", map[string]any{"scope": scope})
	_ = c.JSON(envelope{Data: a.githubOAuthView(ctx)})
}

func (a *API) startGitHubOAuth(c iris.Context) {
	ctx := c.Request().Context()
	clientID, scope := a.oauthSettings(ctx)
	if clientID == "" {
		a.badRequest(c, "还没有配置 GitHub OAuth App 的 Client ID")
		return
	}
	code, err := requestDeviceCode(ctx, clientID, scope)
	if err != nil {
		a.badRequest(c, "向 GitHub 申请设备码失败："+err.Error())
		return
	}
	flowID := newFlowID()
	interval := time.Duration(code.Interval) * time.Second
	a.oauthFlows.mu.Lock()
	now := time.Now()
	for id, f := range a.oauthFlows.flows {
		if now.After(f.expiresAt) {
			delete(a.oauthFlows.flows, id)
		}
	}
	a.oauthFlows.flows[flowID] = &githubOAuthFlow{
		clientID:   clientID,
		deviceCode: code.DeviceCode,
		interval:   interval,
		expiresAt:  now.Add(time.Duration(code.ExpiresIn) * time.Second),
	}
	a.oauthFlows.mu.Unlock()
	a.audit(ctx, c, "github.oauth.start", "setting", githubOAuthClientKey, nil, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{
		"flow_id": flowID, "user_code": code.UserCode, "verification_uri": code.VerificationURI,
		"expires_in": code.ExpiresIn, "interval": code.Interval,
	}})
}

func (a *API) pollGitHubOAuth(c iris.Context) {
	var in struct {
		FlowID string `json:"flow_id"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	flowID := strings.TrimSpace(in.FlowID)
	ctx := c.Request().Context()
	a.oauthFlows.mu.Lock()
	flow, ok := a.oauthFlows.flows[flowID]
	if !ok {
		a.oauthFlows.mu.Unlock()
		a.badRequest(c, "授权流程不存在或已结束")
		return
	}
	if time.Now().After(flow.expiresAt) {
		delete(a.oauthFlows.flows, flowID)
		a.oauthFlows.mu.Unlock()
		_ = c.JSON(envelope{Data: iris.Map{"status": "expired"}})
		return
	}
	if !flow.lastPoll.IsZero() && time.Since(flow.lastPoll) < flow.interval {
		a.oauthFlows.mu.Unlock()
		_ = c.JSON(envelope{Data: iris.Map{"status": "pending"}})
		return
	}
	flow.lastPoll = time.Now()
	clientID, deviceCode := flow.clientID, flow.deviceCode
	a.oauthFlows.mu.Unlock()

	tok, err := pollAccessToken(ctx, clientID, deviceCode)
	if err != nil {
		a.badRequest(c, "轮询 GitHub 授权失败："+err.Error())
		return
	}
	switch tok.Error {
	case "authorization_pending":
		_ = c.JSON(envelope{Data: iris.Map{"status": "pending"}})
		return
	case "slow_down":
		a.oauthFlows.mu.Lock()
		if f, ok := a.oauthFlows.flows[flowID]; ok {
			f.interval += 5 * time.Second
		}
		a.oauthFlows.mu.Unlock()
		_ = c.JSON(envelope{Data: iris.Map{"status": "pending"}})
		return
	case "expired_token":
		a.oauthFlows.mu.Lock()
		delete(a.oauthFlows.flows, flowID)
		a.oauthFlows.mu.Unlock()
		_ = c.JSON(envelope{Data: iris.Map{"status": "expired"}})
		return
	case "access_denied":
		a.oauthFlows.mu.Lock()
		delete(a.oauthFlows.flows, flowID)
		a.oauthFlows.mu.Unlock()
		_ = c.JSON(envelope{Data: iris.Map{"status": "denied", "message": tok.ErrorDescription}})
		return
	}
	if tok.AccessToken == "" {
		a.badRequest(c, "GitHub 未返回 access_token")
		return
	}
	login, err := fetchGitHubLogin(ctx, tok.AccessToken)
	if err != nil {
		a.badRequest(c, "校验 GitHub token 失败："+err.Error())
		return
	}
	meta := map[string]any{
		"login": login, "scope": tok.Scope, "source": "oauth_device",
		"authorized_at": time.Now().UTC().Format(time.RFC3339),
	}
	cred := a.findOAuthCredential(ctx)
	if cred == nil {
		cred, err = a.store.CreateCredential(ctx, nil, store.CredentialGitHubPAT, githubOAuthCredentialName, tok.AccessToken, meta)
	} else {
		_, err = a.store.RotateCredential(ctx, cred.ID, tok.AccessToken)
		if err == nil {
			err = a.store.UpdateCredentialMetadata(ctx, cred.ID, meta)
		}
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	a.oauthFlows.mu.Lock()
	delete(a.oauthFlows.flows, flowID)
	a.oauthFlows.mu.Unlock()
	a.audit(ctx, c, "github.oauth.authorize", "credential", cred.ID, nil, "ok", map[string]any{"login": login, "scope": tok.Scope})
	_ = c.JSON(envelope{Data: iris.Map{
		"status": "authorized", "login": login, "credential_id": cred.ID, "scope": tok.Scope,
	}})
}

func (a *API) deleteGitHubOAuth(c iris.Context) {
	ctx := c.Request().Context()
	cred := a.findOAuthCredential(ctx)
	if cred == nil {
		c.StatusCode(http.StatusNoContent)
		return
	}
	if err := a.store.DeleteCredential(ctx, cred.ID); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "github.oauth.delete", "credential", cred.ID, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}
