package adminapi

import (
	"context"
	"fmt"
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
}

type githubRepoListView struct {
	Connected bool             `json:"connected"`
	Account   string           `json:"account"`
	AppSlug   string           `json:"app_slug"`
	Source    string           `json:"source"`
	Repos     []githubRepoView `json:"repos"`
	Error     string           `json:"error,omitempty"`
	FetchedAt string           `json:"fetched_at"`
}

func (a *API) registerGitHubRepos(party iris.Party) {
	party.Get("/github/repositories", a.listGitHubRepositories)
}

// listGitHubRepositories 用已保存的 GitHub App（优先）或 PAT 列出账号可见的仓库。
func (a *API) listGitHubRepositories(c iris.Context) {
	ctx := c.Request().Context()
	view := githubRepoListView{Repos: []githubRepoView{}, FetchedAt: time.Now().Format(time.RFC3339)}
	creds, err := a.store.ListCredentials(ctx, nil)
	if err != nil {
		a.fail(c, err)
		return
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
		repos, err := fetchGitHubInstallationRepos(ctx, apiBase, token)
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
		view.Repos = repos
		_ = c.JSON(envelope{Data: view})
		return
	}

	for _, cred := range creds {
		if !cred.Enabled || cred.Kind != store.CredentialGitHubPAT || cred.ProjectID != nil {
			continue
		}
		_, secret, meta, err := a.store.GetSecret(ctx, cred.ID)
		if err != nil {
			lastErr = err
			continue
		}
		apiBase := metaStr(meta, "api_base", github.DefaultAPIBase)
		login, repos, err := fetchGitHubUserRepos(ctx, apiBase, secret)
		if err != nil {
			lastErr = err
			continue
		}
		view.Connected = true
		view.Source = "pat"
		view.Account = login
		view.Repos = repos
		_ = c.JSON(envelope{Data: view})
		return
	}

	if lastErr != nil {
		view.Error = lastErr.Error()
	} else {
		view.Error = "还没有可用的 GitHub 授权，请先连接 GitHub"
	}
	_ = c.JSON(envelope{Data: view})
}

// newGitHubClient 基于 go-github 构造客户端；企业版通过 api_base 覆盖默认端点。
func newGitHubClient(apiBase, token string) (*gh.Client, error) {
	client := gh.NewClient(nil).WithAuthToken(token)
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
func fetchGitHubInstallationRepos(ctx context.Context, apiBase, token string) ([]githubRepoView, error) {
	client, err := newGitHubClient(apiBase, token)
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
func fetchGitHubUserRepos(ctx context.Context, apiBase, token string) (string, []githubRepoView, error) {
	client, err := newGitHubClient(apiBase, token)
	if err != nil {
		return "", nil, fmt.Errorf("构造 GitHub 客户端失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", nil, githubErr("读取 GitHub 账号失败", err)
	}
	login := user.GetLogin()
	repos := make([]githubRepoView, 0, 100)
	for page := 1; page <= 10; page++ {
		items, resp, err := client.Repositories.ListByAuthenticatedUser(ctx, &gh.RepositoryListByAuthenticatedUserOptions{
			ListOptions: gh.ListOptions{PerPage: 100, Page: page},
			Affiliation: "owner,collaborator,organization_member",
			Sort:        "updated",
		})
		if err != nil {
			return "", nil, githubErr("读取 GitHub 仓库失败", err)
		}
		for _, repo := range items {
			repos = append(repos, repoView(repo))
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
	}
	return login, repos, nil
}
