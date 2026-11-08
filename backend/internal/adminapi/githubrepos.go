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

type githubAPIRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Language      string `json:"language"`
	Stars         int    `json:"stargazers_count"`
	OpenIssues    int    `json:"open_issues_count"`
	UpdatedAt     string `json:"updated_at"`
	HTMLURL       string `json:"html_url"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
}

func (r githubAPIRepo) view() githubRepoView {
	return githubRepoView{
		ID: r.ID, FullName: r.FullName, Name: r.Name, Owner: r.Owner.Login,
		Private: r.Private, Description: r.Description, DefaultBranch: r.DefaultBranch,
		Language: r.Language, Stars: r.Stars, OpenIssues: r.OpenIssues, UpdatedAt: r.UpdatedAt,
		HTMLURL: r.HTMLURL, Archived: r.Archived, Fork: r.Fork,
	}
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
		login, err := fetchGitHubLoginName(ctx, apiBase, secret)
		if err != nil {
			lastErr = err
			continue
		}
		repos, err := fetchGitHubUserRepos(ctx, apiBase, secret)
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

func fetchGitHubJSON(ctx context.Context, apiBase, token, path string, out any) error {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = github.DefaultAPIBase
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 GitHub 失败：%w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub 返回 %d：%s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func fetchGitHubInstallationRepos(ctx context.Context, apiBase, token string) ([]githubRepoView, error) {
	var payload struct {
		Repositories []githubAPIRepo `json:"repositories"`
	}
	if err := fetchGitHubJSON(ctx, apiBase, token, "/installation/repositories?per_page=100", &payload); err != nil {
		return nil, err
	}
	repos := make([]githubRepoView, 0, len(payload.Repositories))
	for _, item := range payload.Repositories {
		repos = append(repos, item.view())
	}
	return repos, nil
}

func fetchGitHubUserRepos(ctx context.Context, apiBase, token string) ([]githubRepoView, error) {
	var payload []githubAPIRepo
	if err := fetchGitHubJSON(ctx, apiBase, token, "/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member", &payload); err != nil {
		return nil, err
	}
	repos := make([]githubRepoView, 0, len(payload))
	for _, item := range payload {
		repos = append(repos, item.view())
	}
	return repos, nil
}

func fetchGitHubLoginName(ctx context.Context, apiBase, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := fetchGitHubJSON(ctx, apiBase, token, "/user", &user); err != nil {
		return "", err
	}
	return user.Login, nil
}
