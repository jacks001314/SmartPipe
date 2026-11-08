package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/store"
)

// githubRepoView 是仓库列表页需要的仓库基本信息。
type githubRepoView struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Language      string `json:"language"`
	Stars         int    `json:"stars"`
	OpenIssues    int    `json:"open_issues"`
	UpdatedAt     string `json:"updated_at"`
	HTMLURL       string `json:"html_url"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	// Source 标记这条仓库来自 App 安装（app）还是账号授权（user）。
	Source string `json:"source"`
	// UserVisible 表示账号授权（本人/协作者）这条路径也能看到它。
	UserVisible bool `json:"user_visible"`
}

type githubRepoListView struct {
	Connected       bool             `json:"connected"`
	Account         string           `json:"account"`
	AppSlug         string           `json:"app_slug"`
	Source          string           `json:"source"`
	OAuthLogin      string           `json:"oauth_login"`
	OAuthAuthorized bool             `json:"oauth_authorized"`
	OAuthScope      string           `json:"oauth_scope"`
	OAuthScopeLive  string           `json:"oauth_scope_live,omitempty"`
	TokenSource     string           `json:"token_source"`
	UserRepoCount   int              `json:"user_repo_count"`
	UserError       string           `json:"user_error,omitempty"`
	Repos           []githubRepoView `json:"repos"`
	Warning         string           `json:"warning,omitempty"`
	Error           string           `json:"error,omitempty"`
	FetchedAt       string           `json:"fetched_at"`
}

func (a *API) registerGitHubRepos(party iris.Party) {
	party.Get("/github/repositories", a.listGitHubRepositories)
}

// listGitHubRepositories 合并两类来源的仓库：
//  1. GitHub App 安装授权范围（自动化、webhook 用）；
//  2. 账号 OAuth/PAT 授权（能拿到本人、协作者和组织里被邀请的仓库）。
//
// 只有 App 安装授权看不到“别人仓库里邀请你参与”的项目，因此两路必须合并展示。
func (a *API) listGitHubRepositories(c iris.Context) {
	ctx := c.Request().Context()
	view := githubRepoListView{Repos: []githubRepoView{}, FetchedAt: time.Now().Format(time.RFC3339)}
	creds, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		a.fail(c, err)
		return
	}

	index := map[string]int{}
	addRepos := func(list []githubRepoView, source string) {
		view.Repos = mergeGitHubRepoViews(view.Repos, index, list, source)
	}

	var lastErr error
	for _, cred := range creds {
		if !cred.Enabled || cred.Kind != store.CredentialGitHubApp || cred.ProjectID != nil {
			continue
		}
		_, secret, meta, err := a.store.GetSecret(ctx, cred.ID)
		if err != nil {
			lastErr = err
			continue
		}
		appID := metaStr(meta, "app_id", "")
		installID := metaStr(meta, "installation_id", "")
		apiBase := metaStr(meta, "api_base", github.DefaultAPIBase)
		if appID == "" || installID == "" {
			lastErr = fmt.Errorf("GitHub App 凭据缺少 app_id/installation_id")
			continue
		}
		token, _, err := github.InstallationToken(ctx, apiBase, appID, installID, secret, time.Now())
		if err != nil {
			lastErr = err
			continue
		}
		repos, err := a.fetchGitHubInstallationRepos(ctx, apiBase, token)
		if err != nil {
			lastErr = err
			continue
		}
		view.Connected = true
		view.Source = "app"
		view.AppSlug = metaStr(meta, "slug", "")
		view.Account = metaStr(meta, "owner", "")
		if view.Account == "" && len(repos) > 0 {
			view.Account = repos[0].Owner
		}
		addRepos(repos, "app")
		break
	}

	// 账号凭据：优先手工 PAT（经典 PAT 勾选 repo 能看到全部协作/邀请仓库），
	// 其次设备码 OAuth（GitHub App 用户授权，权限由 App 决定）。
	userToken := ""
	userLogin := ""
	var patCred *store.Credential
	for i := range creds {
		cred := &creds[i]
		if !cred.Enabled || cred.Kind != store.CredentialGitHubPAT || cred.ProjectID != nil {
			continue
		}
		if src, _ := cred.Metadata["source"].(string); src == "oauth_device" {
			continue
		}
		patCred = cred
		if kind, secret, _, err := a.store.GetSecret(ctx, cred.ID); err == nil && kind == store.CredentialGitHubPAT {
			userToken = secret
			userLogin, _ = cred.Metadata["login"].(string)
			view.TokenSource = "pat"
			view.OAuthScope, _ = cred.Metadata["scope"].(string)
		}
		break
	}
	oauthCred := a.findOAuthCredential(ctx)
	if oauthCred != nil && oauthCred.Enabled {
		view.OAuthAuthorized = true
		if view.OAuthScope == "" {
			view.OAuthScope, _ = oauthCred.Metadata["scope"].(string)
		}
		if userToken == "" {
			if kind, secret, _, err := a.store.GetSecret(ctx, oauthCred.ID); err == nil && kind == store.CredentialGitHubPAT {
				userToken = secret
				userLogin, _ = oauthCred.Metadata["login"].(string)
				view.TokenSource = "oauth"
				view.OAuthScope, _ = oauthCred.Metadata["scope"].(string)
			}
		}
	}
	if userToken != "" {
		apiBase := github.DefaultAPIBase
		login, scope, repos, err := a.fetchGitHubUserRepos(ctx, apiBase, userToken)
		if err != nil {
			lastErr = err
			view.UserError = err.Error()
		} else {
			view.Connected = true
			if view.Source == "" {
				view.Source = "user"
			}
			if userLogin == "" {
				userLogin = login
			}
			view.OAuthLogin = userLogin
			if view.Account == "" {
				view.Account = login
			}
			view.UserRepoCount = len(repos)
			if scope != "" {
				view.OAuthScopeLive = scope
			}
			addRepos(repos, "user")
			privateFromUser := 0
			for _, repo := range repos {
				if repo.Private {
					privateFromUser++
				}
			}
			if !scopeHasRepoAccess(scope) && privateFromUser == 0 {
				hint := "别人仓库里邀请你参与的私有仓库看不到。"
				if view.TokenSource == "pat" {
					hint += "当前 PAT 没有 repo 权限，请新建一个勾选 repo 的经典 Token 后重新保存。"
				} else {
					hint += "GitHub App 用户授权拿不到 repo 权限，请改用带 repo 权限的经典 PAT（推荐），或部署方配置自建 OAuth App 后重新授权。"
				}
				view.Warning = "账号授权只读到公开仓库（权限：" + scopeLabel(scope) + "），" + hint
			}
		}
	} else if view.OAuthAuthorized {
		view.UserError = "账号授权存在但没有读到可用 token"
	}
	if view.OAuthScopeLive != "" && view.OAuthScopeLive != view.OAuthScope {
		view.OAuthScope = view.OAuthScopeLive
		target := oauthCred
		if view.TokenSource == "pat" {
			target = patCred
		}
		if target != nil {
			meta := map[string]any{}
			for k, v := range target.Metadata {
				meta[k] = v
			}
			meta["scope"] = view.OAuthScopeLive
			_ = a.store.UpdateCredentialMetadata(ctx, target.ID, meta)
		}
	}

	if !view.Connected {
		if lastErr != nil {
			view.Error = lastErr.Error()
		} else {
			view.Error = "还没有可用的 GitHub 授权，请先连接 GitHub"
		}
	}
	_ = c.JSON(envelope{Data: view})
}

// mergeGitHubRepoViews 合并两路仓库：App 安装范围优先，账号授权补充被邀请的仓库。
func mergeGitHubRepoViews(dst []githubRepoView, index map[string]int, list []githubRepoView, source string) []githubRepoView {
	for _, repo := range list {
		repo.Source = source
		key := strings.ToLower(repo.FullName)
		if existing, ok := index[key]; ok {
			if dst[existing].Source != "app" && source == "app" {
				kept := dst[existing]
				dst[existing] = repo
				dst[existing].UserVisible = kept.UserVisible || kept.Source == "user"
			}
			if source == "user" {
				dst[existing].UserVisible = true
			}
			continue
		}
		index[key] = len(dst)
		dst = append(dst, repo)
	}
	return dst
}

// newGitHubClient 基于 go-github 构造客户端；企业版通过 api_base 覆盖默认端点。
func newGitHubClient(httpClient *http.Client, apiBase, token string) (*gh.Client, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	client := gh.NewClient(httpClient).WithAuthToken(token)
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" || base == strings.TrimRight(github.DefaultAPIBase, "/") {
		return client, nil
	}
	return client.WithEnterpriseURLs(base, base)
}

func githubErr(op string, err error) error {
	var respErr *gh.ErrorResponse
	if ok := asGitHubError(err, &respErr); ok {
		return fmt.Errorf("%s：GitHub 返回 %d", op, respErr.Response.StatusCode)
	}
	return fmt.Errorf("%s：%w", op, err)
}

func asGitHubError(err error, target **gh.ErrorResponse) bool {
	for err != nil {
		if typed, ok := err.(*gh.ErrorResponse); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func repoView(repo *gh.Repository) githubRepoView {
	return githubRepoView{
		ID:            repo.GetID(),
		FullName:      repo.GetFullName(),
		Name:          repo.GetName(),
		Owner:         repo.GetOwner().GetLogin(),
		Private:       repo.GetPrivate(),
		Description:   repo.GetDescription(),
		DefaultBranch: repo.GetDefaultBranch(),
		Language:      repo.GetLanguage(),
		Stars:         repo.GetStargazersCount(),
		OpenIssues:    repo.GetOpenIssuesCount(),
		UpdatedAt:     repo.GetUpdatedAt().Time.Format(time.RFC3339),
		HTMLURL:       repo.GetHTMLURL(),
		Archived:      repo.GetArchived(),
		Fork:          repo.GetFork(),
	}
}

// fetchGitHubInstallationRepos 读取 GitHub App 安装授权范围内的仓库。
func (a *API) fetchGitHubInstallationRepos(ctx context.Context, apiBase, token string) ([]githubRepoView, error) {
	client, err := newGitHubClient(a.currentGitHubClient(), apiBase, token)
	if err != nil {
		return nil, fmt.Errorf("构造 GitHub 客户端失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	repos := make([]githubRepoView, 0, 100)
	for page := 1; page <= 10; page++ {
		result, resp, err := client.Apps.ListRepos(ctx, &gh.ListOptions{PerPage: 100, Page: page})
		if err != nil {
			return nil, githubErr("读取 GitHub App 仓库失败", err)
		}
		for _, repo := range result.Repositories {
			repos = append(repos, repoView(repo))
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
	}
	return repos, nil
}

// fetchGitHubUserRepos 用 PAT 读取账号本人、协作者与组织内可见的仓库。
func (a *API) fetchGitHubUserRepos(ctx context.Context, apiBase, token string) (string, string, []githubRepoView, error) {
	client, err := newGitHubClient(a.currentGitHubClient(), apiBase, token)
	if err != nil {
		return "", "", nil, fmt.Errorf("构造 GitHub 客户端失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	user, userResp, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", "", nil, githubErr("读取 GitHub 账号失败", err)
	}
	login := user.GetLogin()
	scope := ""
	if userResp != nil {
		scope = strings.TrimSpace(userResp.Header.Get("X-OAuth-Scopes"))
		if scope == "" {
			scope = strings.TrimSpace(userResp.Header.Get("X-Accepted-GitHub-Permissions"))
		}
	}
	repos := make([]githubRepoView, 0, 100)
	for page := 1; page <= 10; page++ {
		items, resp, err := client.Repositories.ListByAuthenticatedUser(ctx, &gh.RepositoryListByAuthenticatedUserOptions{
			ListOptions: gh.ListOptions{PerPage: 100, Page: page},
			Affiliation: "owner,collaborator,organization_member",
			Sort:        "updated",
		})
		if err != nil {
			return "", "", nil, githubErr("读取 GitHub 仓库失败", err)
		}
		if scope == "" && resp != nil {
			scope = strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes"))
			if scope == "" {
				scope = strings.TrimSpace(resp.Header.Get("X-Accepted-GitHub-Permissions"))
			}
		}
		for _, repo := range items {
			repos = append(repos, repoView(repo))
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
	}
	return login, scope, repos, nil
}

// scopeHasRepoAccess 判断 OAuth 授权是否拿到 repo 权限；细粒度 PAT 不返回 scope 头，
// 由是否读到私有仓库来兜底判断。
func scopeHasRepoAccess(scope string) bool {
	for _, item := range strings.Split(scope, ",") {
		if strings.TrimSpace(item) == "repo" {
			return true
		}
	}
	return false
}

func scopeLabel(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "无 scope"
	}
	return scope
}
