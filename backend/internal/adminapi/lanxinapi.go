package adminapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
	lanxinsdk "smartpipe/sdks/lanxin"
)

// registerLanxin 挂载多机器人管理、自动群同步与消息查询。
func (a *API) registerLanxin(party iris.Party) {
	party.Get("/lanxin/bots", a.listLanxinBots)
	party.Post("/lanxin/bots", a.createLanxinBot)
	party.Post("/lanxin/bots/sync-groups", a.syncAllLanxinGroups)
	party.Get("/lanxin/bots/{id}", a.getLanxinBot)
	party.Patch("/lanxin/bots/{id}", a.updateLanxinBot)
	party.Delete("/lanxin/bots/{id}", a.deleteLanxinBot)
	party.Post("/lanxin/bots/{id}/enabled", a.setLanxinBotEnabled)
	party.Post("/lanxin/bots/{id}/test", a.testLanxinBot)
	party.Post("/lanxin/bots/{id}/sync-groups", a.syncOneLanxinBot)
	party.Get("/lanxin/bots/{id}/messages", a.listLanxinBotMessages)
	party.Get("/lanxin/groups", a.listLanxinGroups)
	party.Post("/lanxin/groups/policy", a.updateLanxinGroupPolicy)
	party.Get("/lanxin/messages", a.listLanxinMessages)

	// 兼容旧接口：绑定已经从蓝信页移到项目创建/编辑；这些接口保留给已有自动化调用。
	party.Post("/lanxin/groups/bind", a.bindLanxinGroup)
	party.Delete("/lanxin/groups/{group}", a.unbindLanxinGroup)
}

// registerLanxinInternal 供 bot 进程读取多机器人配置并上报连接状态。
func (a *API) registerLanxinInternal(app *iris.Application) {
	app.Get("/internal/lanxin/bots", a.internalLanxinBots)
	app.Post("/internal/lanxin/bots/{id}/status", a.internalLanxinBotStatus)
	// 单机器人旧协议保留，避免滚动升级时短暂中断。
	app.Get("/internal/lanxin/config", a.internalLanxinConfig)
}

type lanxinBotView struct {
	store.LanxinBot
	SecretMask      string     `json:"secret_mask"`
	HasSecret       bool       `json:"has_secret"`
	LastTestedAt    *time.Time `json:"last_tested_at"`
	LastTestOK      *bool      `json:"last_test_ok"`
	LastTestMessage string     `json:"last_test_message"`
}

type lanxinBotDetail struct {
	Bot      lanxinBotView         `json:"bot"`
	Groups   []store.LanxinGroup   `json:"groups"`
	Messages []store.LanxinMessage `json:"messages"`
}

type lanxinBotInput struct {
	Name        string `json:"name"`
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	APIBase     string `json:"api_base"`
	RequireAtMe *bool  `json:"require_at_me"`
	Enabled     *bool  `json:"enabled"`
}

func lanxinView(bot store.LanxinBot, cred *store.Credential) lanxinBotView {
	view := lanxinBotView{LanxinBot: bot, HasSecret: cred != nil, SecretMask: "****"}
	if cred != nil {
		view.SecretMask = cred.Mask
		view.LastTestedAt = cred.LastTestedAt
		view.LastTestOK = cred.LastTestOK
		view.LastTestMessage = cred.LastTestMessage
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

func (a *API) credentialsByID(ctx context.Context) map[string]*store.Credential {
	items, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		return map[string]*store.Credential{}
	}
	out := make(map[string]*store.Credential, len(items))
	for i := range items {
		out[items[i].ID] = &items[i]
	}
	return out
}

func (a *API) listLanxinBots(c iris.Context) {
	ctx := c.Request().Context()
	if truthy(c.URLParam("sync")) {
		_ = a.syncAllBots(ctx)
	}
	items, err := a.store.ListLanxinBots(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	creds := a.credentialsByID(ctx)
	out := make([]lanxinBotView, 0, len(items))
	for _, item := range items {
		out = append(out, lanxinView(item, creds[item.CredentialID]))
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) createLanxinBot(c iris.Context) {
	var in lanxinBotInput
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.AppID = strings.TrimSpace(in.AppID)
	in.AppSecret = strings.TrimSpace(in.AppSecret)
	in.APIBase = lanxinsdk.NormalizeAPIBase(in.APIBase)
	if in.APIBase == "" {
		in.APIBase = lanxinsdk.DefaultAPIBase
	}
	if in.Name == "" || in.AppID == "" || in.AppSecret == "" {
		a.badRequest(c, "机器人名称、App ID 和 App Secret 都不能为空")
		return
	}
	if strings.HasPrefix(in.AppSecret, "http://") || strings.HasPrefix(in.AppSecret, "https://") {
		a.badRequest(c, "App Secret 填成了网关地址，请填写蓝信应用凭证里的 App Secret")
		return
	}
	requireAt := true
	if in.RequireAtMe != nil {
		requireAt = *in.RequireAtMe
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx := c.Request().Context()
	meta := map[string]any{"app_id": in.AppID, "api_base": in.APIBase, "bot_name": in.Name, "source": "bot"}
	cred, err := a.store.CreateCredential(ctx, nil, store.CredentialLanxin, "lanxin-bot-"+newFlowID()[:10], in.AppSecret, meta)
	if err != nil {
		a.fail(c, err)
		return
	}
	bot, err := a.store.CreateLanxinBot(ctx, store.LanxinBot{
		Name: in.Name, CredentialID: cred.ID, AppID: in.AppID, APIBase: in.APIBase,
		RequireAtMe: requireAt, Enabled: enabled, Status: "disconnected",
	})
	if err != nil {
		_ = a.store.DeleteCredential(ctx, cred.ID)
		a.fail(c, err)
		return
	}
	// 添加机器人就同步群；群权限不足时机器人仍保存，错误展示在详情卡片。
	syncErr := a.syncLanxinBotGroups(ctx, bot)
	bot, _ = a.store.GetLanxinBot(ctx, bot.ID)
	if syncErr != nil {
		_ = a.store.SetLanxinBotSyncResult(ctx, bot.ID, syncErr.Error())
	}
	a.audit(ctx, c, "lanxin.bot.create", "lanxin_bot", bot.ID, nil, "ok", map[string]any{"app_id": in.AppID, "name": in.Name})
	updated, _ := a.store.GetLanxinBot(ctx, bot.ID)
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: lanxinView(*updated, cred)})
}

func (a *API) getLanxinBot(c iris.Context) {
	ctx := c.Request().Context()
	bot, err := a.store.GetLanxinBot(ctx, c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	if truthy(c.URLParam("sync")) {
		_ = a.syncLanxinBotGroups(ctx, bot)
		bot, _ = a.store.GetLanxinBot(ctx, bot.ID)
	}
	groups, err := a.store.ListLanxinGroups(ctx, bot.ID, false)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.enrichLanxinGroups(ctx, groups)
	messages, err := a.store.ListLanxinMessagesByBot(ctx, bot.ID, 100)
	if err != nil {
		a.fail(c, err)
		return
	}
	creds := a.credentialsByID(ctx)
	_ = c.JSON(envelope{Data: lanxinBotDetail{Bot: lanxinView(*bot, creds[bot.CredentialID]), Groups: groups, Messages: messages}})
}

func (a *API) updateLanxinBot(c iris.Context) {
	ctx := c.Request().Context()
	bot, err := a.store.GetLanxinBot(ctx, c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	var in lanxinBotInput
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Name) != "" {
		bot.Name = strings.TrimSpace(in.Name)
	}
	if strings.TrimSpace(in.AppID) != "" {
		bot.AppID = strings.TrimSpace(in.AppID)
	}
	if strings.TrimSpace(in.APIBase) != "" {
		bot.APIBase = lanxinsdk.NormalizeAPIBase(in.APIBase)
	}
	if in.RequireAtMe != nil {
		bot.RequireAtMe = *in.RequireAtMe
	}
	if in.Enabled != nil {
		bot.Enabled = *in.Enabled
	}
	secret := strings.TrimSpace(in.AppSecret)
	if strings.HasPrefix(secret, "http://") || strings.HasPrefix(secret, "https://") {
		a.badRequest(c, "App Secret 填成了网关地址，请填写蓝信应用凭证里的 App Secret")
		return
	}
	if secret != "" {
		if _, err := a.store.RotateCredential(ctx, bot.CredentialID, secret); err != nil {
			a.fail(c, err)
			return
		}
	}
	meta := map[string]any{"app_id": bot.AppID, "api_base": bot.APIBase, "bot_name": bot.Name, "source": "bot"}
	if err := a.store.UpdateCredentialMetadata(ctx, bot.CredentialID, meta); err != nil {
		a.fail(c, err)
		return
	}
	updated, err := a.store.UpdateLanxinBot(ctx, bot)
	if err != nil {
		a.fail(c, err)
		return
	}
	if secret != "" || truthy(c.URLParam("sync")) {
		_ = a.syncLanxinBotGroups(ctx, updated)
	}
	updated, _ = a.store.GetLanxinBot(ctx, updated.ID)
	creds := a.credentialsByID(ctx)
	a.audit(ctx, c, "lanxin.bot.update", "lanxin_bot", updated.ID, nil, "ok", map[string]any{"secret_rotated": secret != ""})
	_ = c.JSON(envelope{Data: lanxinView(*updated, creds[updated.CredentialID])})
}

func (a *API) deleteLanxinBot(c iris.Context) {
	ctx := c.Request().Context()
	bot, err := a.store.GetLanxinBot(ctx, c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	if err := a.store.DeleteLanxinBot(ctx, bot.ID); err != nil {
		a.fail(c, err)
		return
	}
	if err := a.store.DeleteCredential(ctx, bot.CredentialID); err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "lanxin.bot.delete", "lanxin_bot", bot.ID, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) setLanxinBotEnabled(c iris.Context) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	bot, err := a.store.SetLanxinBotEnabled(c.Request().Context(), c.Params().Get("id"), in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "lanxin.bot.enabled", "lanxin_bot", bot.ID, nil, "ok", map[string]any{"enabled": in.Enabled})
	_ = c.JSON(envelope{Data: bot})
}

func (a *API) testLanxinBot(c iris.Context) {
	ctx := c.Request().Context()
	bot, err := a.store.GetLanxinBot(ctx, c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	client, err := a.clientForLanxinBot(ctx, bot)
	if err != nil {
		a.badRequest(c, "读取机器人凭据失败")
		return
	}
	start := time.Now()
	_, tokenErr := client.AppToken(ctx)
	res := lanxinTestResult{OK: tokenErr == nil, Message: "蓝信 app token 获取成功", LatencyMS: time.Since(start).Milliseconds()}
	if tokenErr != nil {
		res.Message = "App Token 获取失败：" + tokenErr.Error()
	} else if syncErr := a.syncLanxinBotGroups(ctx, bot); syncErr != nil {
		res.OK = false
		res.Message += "；群同步失败：" + syncErr.Error()
	}
	updated, _ := a.store.GetLanxinBot(ctx, bot.ID)
	if updated != nil && res.OK {
		res.Message += fmt.Sprintf("；已同步 %d 个群", updated.GroupCount)
	}
	_ = a.store.RecordCredentialTest(ctx, bot.CredentialID, res.OK, res.Message)
	creds := a.credentialsByID(ctx)
	a.audit(ctx, c, "lanxin.bot.test", "lanxin_bot", bot.ID, nil, map[bool]string{true: "ok", false: "failed"}[res.OK], map[string]any{"message": res.Message})
	_ = c.JSON(envelope{Data: iris.Map{"ok": res.OK, "message": res.Message, "latency_ms": res.LatencyMS, "bot": lanxinView(*updated, creds[updated.CredentialID])}})
}

type lanxinTestResult struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

func (a *API) syncOneLanxinBot(c iris.Context) {
	ctx := c.Request().Context()
	bot, err := a.store.GetLanxinBot(ctx, c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	if err := a.syncLanxinBotGroups(ctx, bot); err != nil {
		a.badRequest(c, "同步群失败："+err.Error())
		return
	}
	updated, _ := a.store.GetLanxinBot(ctx, bot.ID)
	groups, _ := a.store.ListLanxinGroups(ctx, bot.ID, false)
	a.enrichLanxinGroups(ctx, groups)
	_ = c.JSON(envelope{Data: iris.Map{"bot": updated, "groups": groups}})
}

func (a *API) syncAllLanxinGroups(c iris.Context) {
	ctx := c.Request().Context()
	errs := a.syncAllBots(ctx)
	_ = c.JSON(envelope{Data: iris.Map{"errors": errs}})
}

func (a *API) listLanxinGroups(c iris.Context) {
	ctx := c.Request().Context()
	if truthy(c.URLParam("refresh")) {
		_ = a.syncAllBots(ctx)
	}
	groups, err := a.store.ListLanxinGroups(ctx, c.URLParam("bot_id"), false)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.enrichLanxinGroups(ctx, groups)
	_ = c.JSON(envelope{Data: groups})
}

func (a *API) listLanxinBotMessages(c iris.Context) {
	limit := c.URLParamIntDefault("limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	items, err := a.store.ListLanxinMessagesByBot(c.Request().Context(), c.Params().Get("id"), limit)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listLanxinMessages(c iris.Context) {
	limit := c.URLParamIntDefault("limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ctx := c.Request().Context()
	botID := strings.TrimSpace(c.URLParam("bot_id"))
	var items []store.LanxinMessage
	var err error
	if botID != "" {
		items, err = a.store.ListLanxinMessagesByBot(ctx, botID, limit)
	} else {
		items, err = a.store.ListLanxinMessages(ctx, limit)
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) enrichLanxinGroups(ctx context.Context, groups []store.LanxinGroup) {
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		return
	}
	for i := range groups {
		for j := range projects {
			p := &projects[j]
			if p.LanxinBotID != "" && p.LanxinBotID != groups[i].BotID {
				continue
			}
			if containsGroup(p.BluexGroupIDs, groups[i].GroupID) {
				id := p.ID
				groups[i].ProjectID = &id
				groups[i].ProjectName = p.Name
				break
			}
		}
	}
}

func (a *API) clientForLanxinBot(ctx context.Context, bot *store.LanxinBot) (*lanxinsdk.Client, error) {
	kind, secret, meta, err := a.store.GetSecret(ctx, bot.CredentialID)
	if err != nil {
		return nil, err
	}
	if kind != store.CredentialLanxin {
		return nil, fmt.Errorf("凭据类型不是 lanxin")
	}
	appID := bot.AppID
	if appID == "" {
		appID = metaStr(meta, "app_id", "")
	}
	apiBase := bot.APIBase
	if apiBase == "" {
		apiBase = metaStr(meta, "api_base", lanxinsdk.DefaultAPIBase)
	}
	return lanxinsdk.New(appID, secret, lanxinsdk.WithAPIBase(apiBase)), nil
}

func (a *API) syncLanxinBotGroups(parent context.Context, bot *store.LanxinBot) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	client, err := a.clientForLanxinBot(ctx, bot)
	if err != nil {
		_ = a.store.SetLanxinBotSyncResult(parent, bot.ID, err.Error())
		return err
	}
	ids := make([]string, 0, 64)
	for offset := 0; ; {
		page, err := client.ListGroups(ctx, offset, 100)
		if err != nil {
			_ = a.store.SetLanxinBotSyncResult(parent, bot.ID, err.Error())
			return err
		}
		ids = append(ids, page.GroupIDs...)
		offset += len(page.GroupIDs)
		if len(page.GroupIDs) == 0 || offset >= page.TotalGroupIDs {
			break
		}
	}
	sort.Strings(ids)
	groups := make([]store.LanxinGroup, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, groupID := range ids {
		if strings.TrimSpace(groupID) == "" {
			continue
		}
		i, groupID := i, groupID
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := client.GroupInfo(ctx, groupID)
			if err != nil {
				errs[i] = err
				groups[i] = store.LanxinGroup{GroupID: groupID, Name: groupID, Active: true}
				return
			}
			groups[i] = store.LanxinGroup{
				GroupID: groupID, Name: info.Name, AvatarURL: info.AvatarURL,
				Description: info.Description, OwnerID: info.Owner.StaffID, OwnerName: info.Owner.Name,
				MemberCount: info.TotalMembers, SendMsgStatus: info.SendMsgStatus, Active: true,
			}
		}()
	}
	wg.Wait()
	filtered := groups[:0]
	for _, g := range groups {
		if g.GroupID != "" {
			filtered = append(filtered, g)
		}
	}
	if err := a.store.ReplaceLanxinGroups(parent, bot.ID, filtered); err != nil {
		_ = a.store.SetLanxinBotSyncResult(parent, bot.ID, err.Error())
		return err
	}
	var firstErr error
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	msg := ""
	if firstErr != nil {
		msg = fmt.Sprintf("已同步 %d 个群，%d 个群详情获取失败：%v", len(filtered), failed, firstErr)
	}
	_ = a.store.SetLanxinBotSyncResult(parent, bot.ID, msg)
	return nil
}

func (a *API) syncAllBots(ctx context.Context) map[string]string {
	bots, err := a.store.ListLanxinBots(ctx)
	if err != nil {
		return map[string]string{"_": err.Error()}
	}
	errs := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range bots {
		bot := bots[i]
		if !bot.Enabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := a.syncLanxinBotGroups(ctx, &bot); err != nil {
				mu.Lock()
				errs[bot.ID] = err.Error()
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errs
}

type lanxinRuntimeConfig struct {
	BotID       string `json:"bot_id"`
	BotName     string `json:"bot_name"`
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	APIBase     string `json:"api_base"`
	RequireAtMe bool   `json:"require_at_me"`
}

func (a *API) internalLanxinBots(c iris.Context) {
	if !a.authorizeBotInternal(c) {
		return
	}
	ctx := c.Request().Context()
	bots, err := a.store.ListLanxinBots(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]lanxinRuntimeConfig, 0, len(bots))
	for i := range bots {
		bot := &bots[i]
		if !bot.Enabled {
			continue
		}
		kind, secret, meta, err := a.store.GetSecret(ctx, bot.CredentialID)
		if err != nil || kind != store.CredentialLanxin {
			continue
		}
		appID := bot.AppID
		if appID == "" {
			appID = metaStr(meta, "app_id", "")
		}
		apiBase := bot.APIBase
		if apiBase == "" {
			apiBase = metaStr(meta, "api_base", lanxinsdk.DefaultAPIBase)
		}
		out = append(out, lanxinRuntimeConfig{BotID: bot.ID, BotName: bot.Name, AppID: appID,
			AppSecret: secret, APIBase: apiBase, RequireAtMe: bot.RequireAtMe})
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) internalLanxinBotStatus(c iris.Context) {
	if !a.authorizeBotInternal(c) {
		return
	}
	var in struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if err := a.store.SetLanxinBotStatus(c.Request().Context(), c.Params().Get("id"), in.Status, in.Error); err != nil {
		a.fail(c, err)
		return
	}
	c.StatusCode(http.StatusNoContent)
}

func (a *API) internalLanxinConfig(c iris.Context) {
	if !a.authorizeBotInternal(c) {
		return
	}
	ctx := c.Request().Context()
	var bot *store.LanxinBot
	if bots, err := a.store.ListLanxinBots(ctx); err == nil && len(bots) > 0 {
		for i := range bots {
			if bots[i].Enabled {
				bot = &bots[i]
				break
			}
		}
	}
	if bot == nil {
		cred, err := a.findLanxinCredential(ctx)
		if err != nil {
			_ = c.JSON(envelope{Data: iris.Map{"app_id": "", "app_secret": "", "api_base": "", "require_at_me": true}})
			return
		}
		_, secret, meta, _ := a.store.GetSecret(ctx, cred.ID)
		_ = c.JSON(envelope{Data: iris.Map{"app_id": metaStr(meta, "app_id", ""), "app_secret": secret,
			"api_base": metaStr(meta, "api_base", ""), "require_at_me": true}})
		return
	}
	kind, secret, meta, err := a.store.GetSecret(ctx, bot.CredentialID)
	if err != nil || kind != store.CredentialLanxin {
		c.StatusCode(http.StatusNotFound)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"app_id": metaStr(meta, "app_id", bot.AppID), "app_secret": secret,
		"api_base": metaStr(meta, "api_base", bot.APIBase), "require_at_me": bot.RequireAtMe}})
}

func (a *API) authorizeBotInternal(c iris.Context) bool {
	if a.botToken == "" {
		c.StatusCode(http.StatusNotFound)
		return false
	}
	token := c.GetHeader("X-Internal-Token")
	if token == "" {
		token = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.botToken)) != 1 {
		c.StatusCode(http.StatusUnauthorized)
		return false
	}
	return true
}

// 旧绑定接口保留给已有自动化；新界面在项目创建/编辑时选择机器人+群。
func (a *API) bindLanxinGroup(c iris.Context) {
	var in struct {
		GroupID   string `json:"group_id"`
		ProjectID string `json:"project_id"`
		BotID     string `json:"bot_id"`
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
		next := removeGroup(splitGroups(p.BluexGroupIDs), group)
		if p.ID == target.ID {
			next = append(next, group)
			if strings.TrimSpace(in.BotID) != "" {
				p.LanxinBotID = strings.TrimSpace(in.BotID)
			}
		} else if p.LanxinBotID == target.LanxinBotID {
			// 同一机器人下保持一群一项目的兼容语义。
		}
		p.BluexGroupIDs = strings.Join(next, ",")
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

func containsGroup(raw, groupID string) bool {
	for _, g := range splitGroups(raw) {
		if strings.EqualFold(g, groupID) {
			return true
		}
	}
	return false
}

func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

type lanxinGroupPolicyInput struct {
	GroupID              string   `json:"group_id"`
	ResponseMode         *string  `json:"response_mode"`
	ShadowMode           *bool    `json:"shadow_mode"`
	ReplyCooldownSeconds *int     `json:"reply_cooldown_seconds"`
	HourlyReplyLimit     *int     `json:"hourly_reply_limit"`
	AutoIssueEnabled     *bool    `json:"auto_issue_enabled"`
	AutoIssueConfidence  *float64 `json:"auto_issue_confidence"`
	DailyAutoIssueLimit  *int     `json:"daily_auto_issue_limit"`
	AnswerConfidence     *float64 `json:"answer_confidence"`
}

func (a *API) updateLanxinGroupPolicy(c iris.Context) {
	var in lanxinGroupPolicyInput
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.GroupID = strings.TrimSpace(in.GroupID)
	if in.GroupID == "" {
		a.badRequest(c, "group_id 必填")
		return
	}
	ctx := c.Request().Context()
	g, err := a.store.GetLanxinGroupPolicy(ctx, in.GroupID)
	if err != nil {
		a.fail(c, err)
		return
	}
	if in.ResponseMode != nil {
		g.ResponseMode = strings.ToLower(strings.TrimSpace(*in.ResponseMode))
	}
	if in.ShadowMode != nil {
		g.ShadowMode = *in.ShadowMode
	}
	if in.ReplyCooldownSeconds != nil {
		g.ReplyCooldownSeconds = *in.ReplyCooldownSeconds
	}
	if in.HourlyReplyLimit != nil {
		g.HourlyReplyLimit = *in.HourlyReplyLimit
	}
	if in.AutoIssueEnabled != nil {
		g.AutoIssueEnabled = *in.AutoIssueEnabled
	}
	if in.AutoIssueConfidence != nil {
		g.AutoIssueConfidence = *in.AutoIssueConfidence
	}
	if in.DailyAutoIssueLimit != nil {
		g.DailyAutoIssueLimit = *in.DailyAutoIssueLimit
	}
	if in.AnswerConfidence != nil {
		g.AnswerConfidence = *in.AnswerConfidence
	}
	updated, err := a.store.UpdateLanxinGroupPolicy(ctx, *g)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "lanxin.group.policy", "lanxin_group", in.GroupID, nil, "ok", map[string]any{
		"mode": updated.ResponseMode, "shadow": updated.ShadowMode,
		"hourly_limit": updated.HourlyReplyLimit, "cooldown_seconds": updated.ReplyCooldownSeconds,
		"auto_issue": updated.AutoIssueEnabled, "auto_confidence": updated.AutoIssueConfidence,
		"daily_auto_limit": updated.DailyAutoIssueLimit, "answer_confidence": updated.AnswerConfidence,
	})
	_ = c.JSON(envelope{Data: updated})
}
