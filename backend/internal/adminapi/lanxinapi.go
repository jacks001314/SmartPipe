package adminapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

// registerLanxin 挂载蓝信机器人配置：应用连接、群绑定和最近消息。
func (a *API) registerLanxin(party iris.Party) {
	party.Get("/lanxin/config", a.getLanxinConfig)
	party.Put("/lanxin/config", a.putLanxinConfig)
	party.Post("/lanxin/test", a.testLanxinConfig)
	party.Get("/lanxin/groups", a.listLanxinGroups)
	party.Post("/lanxin/groups/bind", a.bindLanxinGroup)
	party.Delete("/lanxin/groups/{group}", a.unbindLanxinGroup)
	party.Get("/lanxin/messages", a.listLanxinMessages)
}

// registerLanxinInternal 供 bot 启动时读取配置，使用 bot 内网令牌鉴权。
func (a *API) registerLanxinInternal(app *iris.Application) {
	app.Get("/internal/lanxin/config", a.internalLanxinConfig)
}

type lanxinConfigView struct {
	CredentialID    string     `json:"credential_id"`
	AppID           string     `json:"app_id"`
	APIBase         string     `json:"api_base"`
	SecretMask      string     `json:"secret_mask"`
	HasSecret       bool       `json:"has_secret"`
	RequireAtMe     bool       `json:"require_at_me"`
	LastTestedAt    *time.Time `json:"last_tested_at"`
	LastTestOK      *bool      `json:"last_test_ok"`
	LastTestMessage string     `json:"last_test_message"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

func lanxinFromCredential(cred *store.Credential) *lanxinConfigView {
	view := &lanxinConfigView{}
	if cred == nil {
		return view
	}
	view.CredentialID = cred.ID
	view.SecretMask = cred.Mask
	view.HasSecret = true
	view.LastTestedAt = cred.LastTestedAt
	view.LastTestOK = cred.LastTestOK
	view.LastTestMessage = cred.LastTestMessage
	view.UpdatedAt = &cred.UpdatedAt
	if v, ok := cred.Metadata["app_id"].(string); ok {
		view.AppID = v
	}
	if v, ok := cred.Metadata["api_base"].(string); ok {
		view.APIBase = v
	}
	return view
}

func (a *API) findLanxinCredential(ctx context.Context) (*store.Credential, error) {
	items, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Kind == store.CredentialLanxin {
			return &items[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (a *API) getLanxinConfig(c iris.Context) {
	ctx := c.Request().Context()
	cred, err := a.findLanxinCredential(ctx)
	if err != nil && err != store.ErrNotFound {
		a.fail(c, err)
		return
	}
	view := lanxinFromCredential(cred)
	requireAt, _ := a.store.GetSetting(ctx, "lanxin_require_at_me")
	view.RequireAtMe = requireAt != "false"
	_ = c.JSON(envelope{Data: view})
}

func (a *API) putLanxinConfig(c iris.Context) {
	var in struct {
		AppID       string `json:"app_id"`
		AppSecret   string `json:"app_secret"`
		APIBase     string `json:"api_base"`
		RequireAtMe *bool  `json:"require_at_me"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.AppID = strings.TrimSpace(in.AppID)
	in.APIBase = strings.TrimSpace(in.APIBase)
	if in.APIBase == "" {
		in.APIBase = "https://open.e.lanxin.cn/open/apigw"
	}
	if in.AppID == "" {
		a.badRequest(c, "App ID 不能为空")
		return
	}
	ctx := c.Request().Context()
	meta := map[string]any{"app_id": in.AppID, "api_base": in.APIBase}
	cred, err := a.findLanxinCredential(ctx)
	switch {
	case err == store.ErrNotFound:
		if strings.TrimSpace(in.AppSecret) == "" {
			a.badRequest(c, "首次配置必须填写 App Secret")
			return
		}
		cred, err = a.store.CreateCredential(ctx, nil, store.CredentialLanxin, "lanxin-app", in.AppSecret, meta)
		if err != nil {
			a.fail(c, err)
			return
		}
	case err != nil:
		a.fail(c, err)
		return
	default:
		if strings.TrimSpace(in.AppSecret) != "" {
			if _, err := a.store.RotateCredential(ctx, cred.ID, in.AppSecret); err != nil {
				a.fail(c, err)
				return
			}
		}
		if err := a.store.UpdateCredentialMetadata(ctx, cred.ID, meta); err != nil {
			a.fail(c, err)
			return
		}
	}
	if in.RequireAtMe != nil {
		value := "true"
		if !*in.RequireAtMe {
			value = "false"
		}
		if err := a.store.SetSetting(ctx, "lanxin_require_at_me", value); err != nil {
			a.fail(c, err)
			return
		}
	}
	updated, _ := a.findLanxinCredential(ctx)
	view := lanxinFromCredential(updated)
	view.RequireAtMe = true
	if v, _ := a.store.GetSetting(ctx, "lanxin_require_at_me"); v == "false" {
		view.RequireAtMe = false
	}
	a.audit(ctx, c, "lanxin.config.update", "credential", cred.ID, nil, "ok",
		map[string]any{"app_id": in.AppID, "secret_rotated": strings.TrimSpace(in.AppSecret) != ""})
	_ = c.JSON(envelope{Data: view})
}

func (a *API) testLanxinConfig(c iris.Context) {
	ctx := c.Request().Context()
	cred, err := a.findLanxinCredential(ctx)
	if err != nil {
		a.badRequest(c, "还没有保存蓝信应用配置")
		return
	}
	kind, secret, meta, err := a.store.GetSecret(ctx, cred.ID)
	if err != nil {
		a.badRequest(c, "读取蓝信凭据失败")
		return
	}
	if kind != store.CredentialLanxin {
		a.badRequest(c, "凭据类型不是 lanxin")
		return
	}
	res := a.checker.Dispatch(ctx, kind, secret, meta)
	_ = a.store.RecordCredentialTest(ctx, cred.ID, res.OK, res.Message)
	result := "ok"
	if !res.OK {
		result = "failed"
	}
	a.audit(ctx, c, "lanxin.config.test", "credential", cred.ID, nil, result, map[string]any{"ok": res.OK})
	updated, _ := a.findLanxinCredential(ctx)
	view := lanxinFromCredential(updated)
	requireAt, _ := a.store.GetSetting(ctx, "lanxin_require_at_me")
	view.RequireAtMe = requireAt != "false"
	_ = c.JSON(envelope{Data: iris.Map{
		"ok": res.OK, "message": res.Message, "latency_ms": res.Latency, "config": view,
	}})
}

type lanxinGroupBinding struct {
	GroupID     string `json:"group_id"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Enabled     bool   `json:"enabled"`
}

func (a *API) listLanxinGroups(c iris.Context) {
	projects, err := a.store.ListProjects(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]lanxinGroupBinding, 0, 16)
	for _, p := range projects {
		for _, raw := range strings.Split(p.BluexGroupIDs, ",") {
			group := strings.TrimSpace(raw)
			if group == "" {
				continue
			}
			out = append(out, lanxinGroupBinding{
				GroupID: group, ProjectID: p.ID, ProjectName: p.Name, Enabled: p.Enabled,
			})
		}
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) bindLanxinGroup(c iris.Context) {
	var in struct {
		GroupID   string `json:"group_id"`
		ProjectID string `json:"project_id"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	group := strings.TrimSpace(in.GroupID)
	if group == "" || strings.TrimSpace(in.ProjectID) == "" {
		a.badRequest(c, "群 ID 与项目都不能为空")
		return
	}
	ctx := c.Request().Context()
	target, err := a.store.GetProject(ctx, in.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	for i := range projects {
		p := &projects[i]
		groups := splitGroups(p.BluexGroupIDs)
		next := removeGroup(groups, group)
		if p.ID == target.ID {
			next = append(next, group)
		}
		joined := strings.Join(next, ",")
		if joined == p.BluexGroupIDs {
			continue
		}
		p.BluexGroupIDs = joined
		if _, err := a.store.UpdateProject(ctx, p); err != nil {
			a.fail(c, err)
			return
		}
	}
	a.audit(ctx, c, "lanxin.group.bind", "project", target.ID, &target.ID, "ok", map[string]any{"group": group})
	_ = c.JSON(envelope{Data: iris.Map{"group_id": group, "project_id": target.ID}})
}

func (a *API) unbindLanxinGroup(c iris.Context) {
	group := strings.TrimSpace(c.Params().Get("group"))
	ctx := c.Request().Context()
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	changed := false
	for i := range projects {
		p := &projects[i]
		next := removeGroup(splitGroups(p.BluexGroupIDs), group)
		if len(next) == len(splitGroups(p.BluexGroupIDs)) {
			continue
		}
		p.BluexGroupIDs = strings.Join(next, ",")
		if _, err := a.store.UpdateProject(ctx, p); err != nil {
			a.fail(c, err)
			return
		}
		changed = true
	}
	if !changed {
		a.badRequest(c, "该群没有绑定项目")
		return
	}
	a.audit(ctx, c, "lanxin.group.unbind", "project", group, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) listLanxinMessages(c iris.Context) {
	limit := 50
	items, err := a.store.ListBridgeLinks(c.Request().Context(), limit*3)
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]store.BridgeLink, 0, limit)
	for _, item := range items {
		if !strings.EqualFold(item.Channel, "lanxin") && !strings.EqualFold(item.Channel, "bluex") {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) internalLanxinConfig(c iris.Context) {
	if a.botToken == "" {
		c.StatusCode(http.StatusNotFound)
		return
	}
	token := c.GetHeader("X-Internal-Token")
	if token == "" {
		token = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.botToken)) != 1 {
		c.StatusCode(http.StatusUnauthorized)
		return
	}
	ctx := c.Request().Context()
	cred, err := a.findLanxinCredential(ctx)
	if err != nil {
		c.StatusCode(http.StatusNotFound)
		return
	}
	_, secret, meta, err := a.store.GetSecret(ctx, cred.ID)
	if err != nil {
		c.StatusCode(http.StatusNotFound)
		return
	}
	appID, _ := meta["app_id"].(string)
	apiBase, _ := meta["api_base"].(string)
	requireAt, _ := a.store.GetSetting(ctx, "lanxin_require_at_me")
	_ = c.JSON(envelope{Data: iris.Map{
		"app_id": appID, "app_secret": secret, "api_base": apiBase,
		"require_at_me": requireAt != "false",
	}})
}

func splitGroups(raw string) []string {
	out := make([]string, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		if g := strings.TrimSpace(item); g != "" {
			out = append(out, g)
		}
	}
	return out
}

func removeGroup(groups []string, target string) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if g != target {
			out = append(out, g)
		}
	}
	return out
}
