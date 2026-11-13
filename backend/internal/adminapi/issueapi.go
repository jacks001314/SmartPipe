package adminapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/store"
)

func (a *API) registerIssues(p iris.Party) {
	p.Get("/issues/{number}", a.getIssue)
	p.Post("/issues", a.createIssue)
	p.Get("/issue-search", a.searchIssues)
	p.Get("/issues/{number}/comments", a.listIssueComments)
	p.Post("/issues/{number}/comments", a.commentIssue)
	p.Post("/issues/{number}/labels", a.addIssueLabels)
	p.Post("/issues/{number}/pulls", a.createIssuePull)
	p.Post("/issues/{number}/pulls/{pull}/merge", a.mergeIssuePull)
}

func (a *API) issueTarget(c iris.Context) (*store.Project, int, string, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(c.Params().Get("number")))
	if err != nil || number <= 0 {
		a.badRequest(c, "非法 Issue 编号")
		return nil, 0, "", false
	}
	projectID := strings.TrimSpace(c.URLParam("project_id"))
	if projectID == "" {
		var in struct {
			ProjectID string `json:"project_id"`
		}
		_ = decodeBody(c, &in)
		projectID = strings.TrimSpace(in.ProjectID)
	}
	if projectID == "" {
		a.badRequest(c, "project_id 必填")
		return nil, 0, "", false
	}
	project, err := a.store.GetProject(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return nil, 0, "", false
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	if repo == "" {
		a.badRequest(c, "项目未配置 GitHub 仓库")
		return nil, 0, "", false
	}
	return project, number, repo, true
}

func (a *API) getIssue(c iris.Context) {
	project, number, repo, ok := a.issueTarget(c)
	if !ok {
		return
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	issue, err := client.GetIssue(c.Request().Context(), repo, number)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	_ = c.JSON(envelope{Data: issue})
}

func (a *API) commentIssue(c iris.Context) {
	var in struct {
		ProjectID string `json:"project_id"`
		Body      string `json:"body"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	project, number, repo, ok := a.issueTargetWithBody(c, in.ProjectID)
	if !ok {
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		a.badRequest(c, "body 必填")
		return
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	if err := client.Comment(c.Request().Context(), repo, number, in.Body); err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	a.audit(c.Request().Context(), c, "issue.comment", "issue", strconv.Itoa(number), &project.ID, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{"ok": true, "issue": number}})
}

func (a *API) addIssueLabels(c iris.Context) {
	var in struct {
		ProjectID string   `json:"project_id"`
		Labels    []string `json:"labels"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	project, number, repo, ok := a.issueTargetWithBody(c, in.ProjectID)
	if !ok {
		return
	}
	if len(in.Labels) == 0 {
		a.badRequest(c, "labels 必填")
		return
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	if err := client.AddLabels(c.Request().Context(), repo, number, in.Labels); err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	a.audit(c.Request().Context(), c, "issue.labels", "issue", strconv.Itoa(number), &project.ID, "ok",
		map[string]any{"labels": in.Labels})
	_ = c.JSON(envelope{Data: iris.Map{"ok": true, "issue": number, "labels": in.Labels}})
}

// issueTargetWithBody 在 POST 场景用请求体里的 project_id 解析目标。
func (a *API) issueTargetWithBody(c iris.Context, projectID string) (*store.Project, int, string, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(c.Params().Get("number")))
	if err != nil || number <= 0 {
		a.badRequest(c, "非法 Issue 编号")
		return nil, 0, "", false
	}
	if strings.TrimSpace(projectID) == "" {
		projectID = strings.TrimSpace(c.URLParam("project_id"))
	}
	if projectID == "" {
		a.badRequest(c, "project_id 必填")
		return nil, 0, "", false
	}
	project, err := a.store.GetProject(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return nil, 0, "", false
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	if repo == "" {
		a.badRequest(c, "项目未配置 GitHub 仓库")
		return nil, 0, "", false
	}
	return project, number, repo, true
}

// projectRepo 返回项目用于 Issue/PR 的仓库坐标。
func projectRepo(p *store.Project) string {
	if repo := strings.TrimSpace(p.GitHubRepoURL); repo != "" {
		return repo
	}
	return strings.TrimSpace(p.GitHubIssueRepo)
}

func (a *API) createIssue(c iris.Context) {
	var in struct {
		ProjectID string   `json:"project_id"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Labels    []string `json:"labels"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.ProjectID == "" || in.Title == "" {
		a.badRequest(c, "project_id 与 title 必填")
		return
	}
	project, err := a.store.GetProject(c.Request().Context(), in.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	repo := projectRepo(project)
	if repo == "" {
		a.badRequest(c, "项目未配置 GitHub 仓库")
		return
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	issue, err := client.CreateIssue(c.Request().Context(), repo, in.Title, in.Body, in.Labels)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	a.audit(c.Request().Context(), c, "issue.create", "issue", strconv.Itoa(issue.Number), &project.ID, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{
		"number": issue.Number, "title": issue.Title, "url": issue.HTMLURL,
		"state": issue.State, "body": issue.Body,
	}})
}

func (a *API) searchIssues(c iris.Context) {
	projectID := strings.TrimSpace(c.URLParam("project_id"))
	q := strings.TrimSpace(c.URLParam("q"))
	if projectID == "" || q == "" {
		a.badRequest(c, "project_id 与 q 必填")
		return
	}
	project, err := a.store.GetProject(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	repo := projectRepo(project)
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	issues, err := client.SearchIssues(c.Request().Context(), repo, q, 20)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	out := make([]iris.Map, 0, len(issues))
	for _, it := range issues {
		out = append(out, iris.Map{"number": it.Number, "title": it.Title, "state": it.State, "url": it.HTMLURL})
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) listIssueComments(c iris.Context) {
	project, number, repo, ok := a.issueTarget(c)
	if !ok {
		return
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	comments, err := client.ListComments(c.Request().Context(), repo, number)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	out := make([]iris.Map, 0, len(comments))
	for _, it := range comments {
		out = append(out, iris.Map{"id": it.ID, "author": it.Author, "body": it.Body, "created_at": it.CreatedAt})
	}
	_ = c.JSON(envelope{Data: out})
}

func (a *API) createIssuePull(c iris.Context) {
	var in struct {
		ProjectID string `json:"project_id"`
		Head      string `json:"head"`
		Base      string `json:"base"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		Draft     bool   `json:"draft"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	project, number, repo, ok := a.issueTargetWithBody(c, in.ProjectID)
	if !ok {
		return
	}
	in.Head = strings.TrimSpace(in.Head)
	if in.Head == "" || in.Title == "" {
		a.badRequest(c, "head 与 title 必填")
		return
	}
	base := strings.TrimSpace(in.Base)
	if base == "" {
		base = firstNonEmpty(project.GitHubDefaultBranch, "main")
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	pr, err := client.CreatePullRequest(c.Request().Context(), repo, in.Head, base, in.Title, in.Body, in.Draft)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_error", Message: err.Error()}})
		return
	}
	a.audit(c.Request().Context(), c, "pull.create", "pull", strconv.Itoa(pr.Number), &project.ID, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{"number": pr.Number, "url": pr.HTMLURL, "state": pr.State, "draft": pr.Draft, "issue": number}})
}

// mergeIssuePull 把 worker 的 PR 合入基线分支；失败时把 GitHub 的原因原样返回。
func (a *API) mergeIssuePull(c iris.Context) {
	var in struct {
		ProjectID string `json:"project_id"`
		Title     string `json:"title"`
	}
	_ = decodeBody(c, &in)
	project, number, repo, ok := a.issueTargetWithBody(c, in.ProjectID)
	if !ok {
		return
	}
	pull, err := strconv.Atoi(strings.TrimSpace(c.Params().Get("pull")))
	if err != nil || pull <= 0 {
		a.badRequest(c, "非法 PR 编号")
		return
	}
	client, err := bridge.ClientForRepo(c.Request().Context(), a.store, &project.ID, repo)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = fmt.Sprintf("Issue #%d", number)
	}
	if err := client.MergePullRequest(c.Request().Context(), repo, pull, title); err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "merge_failed", Message: err.Error()}})
		return
	}
	a.audit(c.Request().Context(), c, "pull.merge", "pull", strconv.Itoa(pull), &project.ID, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{"merged": true, "number": pull, "issue": number}})
}
