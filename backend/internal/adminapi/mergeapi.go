package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/gate"
	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/patch"
	"smartpipe/backend/internal/store"
)

func (a *API) registerMerges(p iris.Party) {
	p.Post("/commits", a.createCommit)
	p.Post("/pushes", a.createPush)
	p.Get("/repo-ops", a.listRepoOps)
}

type commitRequest struct {
	ProjectID    string `json:"project_id"`
	IssueRef     string `json:"issue_ref"`
	Repo         string `json:"repo"`
	BaseCommit   string `json:"base_commit"`
	Branch       string `json:"branch"`
	Patch        string `json:"patch"`
	Message      string `json:"message"`
	AuthorName   string `json:"author_name"`
	AuthorEmail  string `json:"author_email"`
	TargetBranch string `json:"target_branch"`
}

type commitResult struct {
	OK           bool   `json:"ok"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	TargetBranch string `json:"target_branch"`
	TargetHead   string `json:"target_head"`
	Message      string `json:"message"`
	Error        string `json:"error"`
}

func (a *API) createCommit(c iris.Context) {
	var in commitRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Repo) == "" || strings.TrimSpace(in.BaseCommit) == "" || strings.TrimSpace(in.Patch) == "" {
		a.badRequest(c, "repo、base_commit、patch 必填")
		return
	}
	var project *store.Project
	var projectID *string
	if strings.TrimSpace(in.ProjectID) != "" {
		p, err := a.store.GetProject(c.Request().Context(), in.ProjectID)
		if err != nil {
			a.fail(c, err)
			return
		}
		project = p
		v := p.ID
		projectID = &v
	}
	// 合并前再查一次审批门禁：复杂 Issue 未批准不允许落地到分支。
	if strings.TrimSpace(in.IssueRef) != "" {
		state, gerr := gate.Check(c.Request().Context(), a.store, project, in.IssueRef)
		if gerr == nil && !state.OK {
			a.audit(c.Request().Context(), c, "repo.commit", "issue", in.IssueRef, projectID, "blocked",
				map[string]any{"reason": state.Reason, "version": state.Version})
			c.StatusCode(http.StatusForbidden)
			_ = c.JSON(envelope{Err: &apiError{Code: "approval_required", Message: state.Reason}})
			return
		}
	}

	branch := strings.TrimSpace(in.Branch)
	if branch == "" && strings.TrimSpace(in.IssueRef) != "" {
		branch = "agent/" + issueDigits(in.IssueRef)
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		message = "fix: " + strings.TrimSpace(in.IssueRef)
	}
	merged, err := (&patch.Verifier{}).CommitAndMerge(c.Request().Context(), patch.MergeRequest{
		Repo: in.Repo, BaseCommit: in.BaseCommit, Branch: branch, Patch: in.Patch,
		Message: message, AuthorName: in.AuthorName, AuthorEmail: in.AuthorEmail,
		TargetBranch: strings.TrimSpace(in.TargetBranch),
	})
	if err != nil {
		a.audit(c.Request().Context(), c, "repo.commit", "issue", in.IssueRef, projectID, "error", map[string]any{"error": err.Error()})
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "commit_failed", Message: err.Error()}})
		return
	}
	res := &commitResult{
		OK: merged.OK, Branch: merged.Branch, Commit: merged.Commit,
		TargetBranch: merged.TargetBranch, TargetHead: merged.TargetHead,
		Message: merged.Message, Error: merged.Error,
	}
	status := "ok"
	if !res.OK {
		status = "rejected"
	}
	_ = a.store.CreateRepoOp(c.Request().Context(), store.RepoOp{
		ProjectID: projectID, Kind: "commit", IssueRef: strings.TrimSpace(in.IssueRef), Branch: res.Branch,
		TargetBranch: res.TargetBranch, Commit: res.Commit, OK: res.OK, Error: res.Error, Repo: shortRepo(in.Repo),
	})
	a.audit(c.Request().Context(), c, "repo.commit", "issue", in.IssueRef, projectID, status,
		map[string]any{"branch": res.Branch, "commit": res.Commit, "target_branch": res.TargetBranch, "ok": res.OK})
	if res.OK && projectID != nil && strings.TrimSpace(in.IssueRef) != "" {
		a.commentIssueQuiet(c.Request().Context(), *projectID, in.IssueRef,
			fmt.Sprintf("已提交到分支 `%s`，commit `%s`（目标分支 `%s`）。", res.Branch, short(res.Commit), res.TargetBranch))
	}
	if !res.OK {
		c.StatusCode(http.StatusUnprocessableEntity)
	}
	_ = c.JSON(envelope{Data: res})
}

type pushRequest struct {
	ProjectID string `json:"project_id"`
	IssueRef  string `json:"issue_ref"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Remote    string `json:"remote"`
}

type pushResult struct {
	OK     bool   `json:"ok"`
	Branch string `json:"branch"`
	Remote string `json:"remote"`
	Before string `json:"before"`
	After  string `json:"after"`
	Commit string `json:"commit"`
	Error  string `json:"error"`
}

func (a *API) createPush(c iris.Context) {
	var in pushRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Repo) == "" || strings.TrimSpace(in.Branch) == "" {
		a.badRequest(c, "repo、branch 必填")
		return
	}
	var project *store.Project
	var projectID *string
	if strings.TrimSpace(in.ProjectID) != "" {
		p, err := a.store.GetProject(c.Request().Context(), in.ProjectID)
		if err != nil {
			a.fail(c, err)
			return
		}
		project = p
		v := p.ID
		projectID = &v
	}
	// 复杂 Issue 未批准时不允许把分支推到远端。
	if strings.TrimSpace(in.IssueRef) != "" {
		state, gerr := gate.Check(c.Request().Context(), a.store, project, in.IssueRef)
		if gerr == nil && !state.OK {
			a.audit(c.Request().Context(), c, "repo.push", "issue", in.IssueRef, projectID, "blocked",
				map[string]any{"reason": state.Reason, "version": state.Version})
			c.StatusCode(http.StatusForbidden)
			_ = c.JSON(envelope{Err: &apiError{Code: "approval_required", Message: state.Reason}})
			return
		}
	}
	// 远端地址与凭据只由服务端解析：Agent 传什么都会被忽略。
	remoteURL, err := a.resolvePushRemote(c.Request().Context(), project, in.Repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "git_credential_unavailable", Message: err.Error()}})
		return
	}
	pushed, err := (&patch.Verifier{}).Push(c.Request().Context(), patch.PushRequest{
		Repo: in.Repo, Branch: in.Branch, Remote: in.Remote, RemoteURL: remoteURL,
	})
	if err != nil {
		a.audit(c.Request().Context(), c, "repo.push", "issue", in.IssueRef, projectID, "error", map[string]any{"error": err.Error()})
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "push_failed", Message: err.Error()}})
		return
	}
	res := &pushResult{OK: pushed.OK, Branch: pushed.Branch, Remote: pushed.Remote,
		Before: pushed.Before, After: pushed.After, Commit: pushed.Commit, Error: pushed.Error}
	status := "ok"
	if !res.OK {
		status = "rejected"
	}
	_ = a.store.CreateRepoOp(c.Request().Context(), store.RepoOp{
		ProjectID: projectID, Kind: "push", IssueRef: strings.TrimSpace(in.IssueRef), Branch: res.Branch,
		Commit: res.Commit, OK: res.OK, Error: res.Error, Repo: shortRepo(in.Repo),
	})
	a.audit(c.Request().Context(), c, "repo.push", "issue", in.IssueRef, projectID, status,
		map[string]any{"branch": res.Branch, "commit": res.Commit, "remote": res.Remote, "ok": res.OK})
	if res.OK && projectID != nil && strings.TrimSpace(in.IssueRef) != "" {
		a.commentIssueQuiet(c.Request().Context(), *projectID, in.IssueRef,
			fmt.Sprintf("已推送分支 `%s` 到远端（commit `%s`）。", res.Branch, short(res.Commit)))
	}
	if !res.OK {
		c.StatusCode(http.StatusUnprocessableEntity)
	}
	_ = c.JSON(envelope{Data: res})
}

func (a *API) listRepoOps(c iris.Context) {
	items, err := a.store.ListRepoOps(c.Request().Context(), intQuery(c, "limit", 100))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

// resolvePushRemote 用项目配置的仓库地址 + 服务端凭据拼出带 token 的远端，token 不出进程。
func (a *API) resolvePushRemote(ctx context.Context, project *store.Project, repo string) (string, error) {
	target := strings.TrimSpace(repo)
	// repo 通常指向本地工作副本；远端以项目配置的仓库地址为准。
	isHTTP := strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
	if !isHTTP && project != nil && strings.TrimSpace(project.GitHubRepoURL) != "" {
		target = strings.TrimSpace(project.GitHubRepoURL)
	}
	if target == "" {
		return "", fmt.Errorf("未提供仓库地址，项目也未配置 github_repo_url")
	}
	// 非 HTTP(S) 地址（本地裸库、SSH）直接交给 verifier，不需要注入 token。
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return target, nil
	}
	if strings.Contains(target, "@") && strings.Contains(target, "://") {
		return target, nil
	}
	projectID := (*string)(nil)
	if project != nil {
		v := project.ID
		projectID = &v
	}
	token, err := gitToken(ctx, a.store, projectID)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String(), nil
}

// gitToken 按项目 → 全局顺序解析可用的 Git/HTTP token。
func gitToken(ctx context.Context, st *store.Store, projectID *string) (string, error) {
	all, err := st.ListCredentials(ctx, nil)
	if err != nil {
		return "", err
	}
	pick := func(kind string) []store.Credential {
		var scoped, global []store.Credential
		for _, c := range all {
			if !c.Enabled || c.Kind != kind {
				continue
			}
			if projectID != nil && c.ProjectID != nil && *c.ProjectID == *projectID {
				scoped = append(scoped, c)
			}
			if c.ProjectID == nil {
				global = append(global, c)
			}
		}
		return append(scoped, global...)
	}
	var lastErr error
	for _, c := range append(pick(store.CredentialGitHubPAT), pick(store.CredentialGitHubApp)...) {
		kind, secret, meta, err := st.GetSecret(ctx, c.ID)
		if err != nil {
			lastErr = err
			continue
		}
		if kind == store.CredentialGitHubPAT {
			return secret, nil
		}
		apiBase := metaStr(meta, "api_base", "https://api.github.com")
		appID := metaStr(meta, "app_id", "")
		installID := metaStr(meta, "installation_id", "")
		if appID == "" || installID == "" {
			lastErr = fmt.Errorf("github_app 凭据缺少 app_id/installation_id")
			continue
		}
		token, _, err := github.InstallationToken(ctx, apiBase, appID, installID, secret, time.Now())
		if err != nil {
			lastErr = err
			continue
		}
		return token, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的 GitHub 凭据（github_pat / github_app）")
	}
	return "", lastErr
}

func (a *API) commentIssueQuiet(ctx context.Context, projectID, issueRef, body string) {
	num := issueDigits(issueRef)
	if num == "" {
		return
	}
	number, err := strconv.Atoi(num)
	if err != nil || number <= 0 {
		return
	}
	project, err := a.store.GetProject(ctx, projectID)
	if err != nil || project == nil {
		return
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	if repo == "" {
		return
	}
	client, err := bridge.ClientFromCredentials(ctx, a.store, &project.ID)
	if err != nil {
		return
	}
	_ = client.Comment(ctx, repo, number, body)
}

func issueDigits(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.TrimSpace(ref[i+1:])
	}
	return ref
}

func short(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// shortRepo 去掉 URL 里的凭据后再落库。
func metaStr(meta map[string]any, key, fallback string) string {
	if meta == nil {
		return fallback
	}
	if v, ok := meta[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func shortRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if i := strings.Index(repo, "://"); i > 0 {
		if j := strings.Index(repo[i+3:], "@"); j >= 0 {
			repo = repo[:i+3] + repo[i+3+j+1:]
		}
	}
	return repo
}
