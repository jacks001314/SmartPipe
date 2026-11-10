package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/store"
)

const githubPATCredentialName = "github-pat"

type githubPATView struct {
	Configured   bool   `json:"configured"`
	Login        string `json:"login"`
	Scope        string `json:"scope"`
	RepoCount    int    `json:"repo_count"`
	PrivateCount int    `json:"private_count"`
	InvitedCount int    `json:"invited_count"`
	Message      string `json:"message"`
}

func (a *API) registerGitHubPAT(party iris.Party) {
	party.Post("/github/token", a.saveGitHubPAT)
	party.Delete("/github/token", a.deleteGitHubPAT)
}

// saveGitHubPAT 保存账号访问令牌（经典 PAT 勾选 repo）。App 安装授权与
// GitHub App 用户授权都看不到“别人仓库里邀请你参与”的项目，PAT 可以。
func (a *API) saveGitHubPAT(c iris.Context) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	token := strings.TrimSpace(in.Token)
	if token == "" {
		a.badRequest(c, "访问令牌不能为空")
		return
	}
	if strings.ContainsAny(token, " \t\r\n") {
		a.badRequest(c, "访问令牌里不应包含空白字符")
		return
	}
	ctx := c.Request().Context()
	login, scope, repos, err := a.fetchGitHubUserRepos(ctx, github.DefaultAPIBase, token)
	if err != nil {
		a.badRequest(c, "令牌校验失败："+err.Error())
		return
	}
	privateCount := 0
	invitedCount := 0
	for _, repo := range repos {
		if repo.Private {
			privateCount++
		}
		if !strings.EqualFold(repo.Owner, login) {
			invitedCount++
		}
	}
	meta := map[string]any{
		"login": login, "scope": scope, "source": "pat_manual",
		"saved_at": time.Now().UTC().Format(time.RFC3339),
	}
	cred := a.findManualPATCredential(ctx)
	if cred == nil {
		cred, err = a.store.CreateCredential(ctx, nil, store.CredentialGitHubPAT, githubPATCredentialName, token, meta)
	} else {
		if _, err = a.store.RotateCredential(ctx, cred.ID, token); err == nil {
			err = a.store.UpdateCredentialMetadata(ctx, cred.ID, meta)
		}
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	// 令牌刚变过，之前缓存的仓库可见性判断不再可信。
	bridge.ResetCredentialCache()
	a.audit(ctx, c, "github.pat.save", "credential", cred.ID, nil, "ok", map[string]any{"login": login, "private": privateCount})
	view := githubPATView{
		Configured: true, Login: login, Scope: scope,
		RepoCount: len(repos), PrivateCount: privateCount, InvitedCount: invitedCount,
	}
	if !scopeHasRepoAccess(scope) && privateCount == 0 {
		view.Message = fmt.Sprintf("令牌可用，但只读到 %d 个公开仓库；请换用勾选 repo 的经典 PAT。", len(repos))
	} else {
		view.Message = fmt.Sprintf("令牌已保存：%d 个仓库，其中私有 %d 个、邀请协作 %d 个。", len(repos), privateCount, invitedCount)
	}
	_ = c.JSON(envelope{Data: view})
}

func (a *API) deleteGitHubPAT(c iris.Context) {
	ctx := c.Request().Context()
	deleted := 0
	for _, item := range a.listManualPATCredentials(ctx) {
		if err := a.store.DeleteCredential(ctx, item.ID); err != nil {
			a.fail(c, err)
			return
		}
		deleted++
	}
	if deleted == 0 {
		c.StatusCode(http.StatusNoContent)
		return
	}
	bridge.ResetCredentialCache()
	_ = c.JSON(envelope{Data: iris.Map{"deleted": deleted}})
}

func (a *API) listManualPATCredentials(ctx context.Context) []*store.Credential {
	items, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		return nil
	}
	out := make([]*store.Credential, 0, len(items))
	for i := range items {
		item := &items[i]
		if item.Kind != store.CredentialGitHubPAT || item.ProjectID != nil {
			continue
		}
		if src, _ := item.Metadata["source"].(string); src == "oauth_device" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (a *API) findManualPATCredential(ctx context.Context) *store.Credential {
	list := a.listManualPATCredentials(ctx)
	if len(list) == 0 {
		return nil
	}
	return list[0]
}
