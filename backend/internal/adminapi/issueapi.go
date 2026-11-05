package adminapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/store"
)

func (a *API) registerIssues(p iris.Party) {
	p.Get("/issues/{number}", a.getIssue)
	p.Post("/issues/{number}/comments", a.commentIssue)
	p.Post("/issues/{number}/labels", a.addIssueLabels)
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
	client, err := bridge.ClientFromCredentials(c.Request().Context(), a.store, &project.ID)
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
	client, err := bridge.ClientFromCredentials(c.Request().Context(), a.store, &project.ID)
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
	client, err := bridge.ClientFromCredentials(c.Request().Context(), a.store, &project.ID)
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
