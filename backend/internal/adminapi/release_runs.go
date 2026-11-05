package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/release"
	"smartpipe/backend/internal/store"
)

// groupNotifier 把发版通知发到项目绑定的蓝信群。
type groupNotifier struct {
	Store *store.Store
	Bot   *bridge.BotNotifier
}

// Notify 依次发到项目配置的每个群，任一失败即返回错误。
func (n *groupNotifier) Notify(ctx context.Context, projectID, text string) error {
	if n.Bot == nil || strings.TrimSpace(n.Bot.BaseURL) == "" {
		return fmt.Errorf("adminapi: bot address is not configured")
	}
	if strings.TrimSpace(projectID) == "" {
		return fmt.Errorf("adminapi: project id is required for notification")
	}
	project, err := n.Store.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	groups := splitCSV(project.BluexGroupIDs)
	if len(groups) == 0 {
		return fmt.Errorf("adminapi: project %s has no bluex group bound", projectID)
	}
	for _, group := range groups {
		if err := n.Bot.Reply(ctx, "group", group, "", text); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) releaseNotifier() *groupNotifier {
	return &groupNotifier{Store: a.store, Bot: bridge.NewBotNotifier(a.botAddr, a.botToken)}
}

func (a *API) registerReleaseRuns(p iris.Party) {
	p.Get("/runs", a.listReleaseRuns)
	p.Get("/runs/{id}", a.getReleaseRun)
	p.Post("/runs", a.createReleaseRun)
	p.Post("/runs/{id}/rollback", a.rollbackReleaseRun)
	p.Get("/candidates", a.listReleaseCandidates)
}

func (a *API) listReleaseRuns(c iris.Context) {
	items, err := a.store.ListReleaseRuns(c.Request().Context(), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getReleaseRun(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.badRequest(c, "非法 id")
		return
	}
	item, err := a.store.GetReleaseRun(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: item})
}

func (a *API) releaseLabels(project *store.Project, override string) (string, string) {
	prefix := strings.TrimSpace(project.LabelsPrefix)
	if prefix == "" {
		prefix = "smartpipe"
	}
	ready, released := prefix+":ready", prefix+":released"
	if strings.TrimSpace(override) != "" {
		ready = strings.TrimSpace(override)
		if idx := strings.LastIndex(ready, ":"); idx > 0 {
			released = ready[:idx] + ":released"
		}
	}
	return ready, released
}

func (a *API) listReleaseCandidates(c iris.Context) {
	projectID := strings.TrimSpace(c.URLParam("project_id"))
	if projectID == "" {
		a.badRequest(c, "project_id 必填")
		return
	}
	project, err := a.store.GetProject(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	if repo == "" {
		a.badRequest(c, "项目未配置 GitHub 仓库")
		return
	}
	ready, _ := a.releaseLabels(project, c.URLParam("label"))
	client, err := bridge.ClientFromCredentials(c.Request().Context(), a.store, &project.ID)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	candidates, err := release.CollectCandidates(c.Request().Context(), client, repo, ready)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "candidates_failed", Message: err.Error()}})
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"repo": repo, "label": ready, "candidates": candidates}})
}

type releaseRunRequest struct {
	ProjectID     string   `json:"project_id"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	Branch        string   `json:"branch"`
	Notes         string   `json:"notes"`
	DryRun        bool     `json:"dry_run"`
	ReadyLabel    string   `json:"ready_label"`
	ArtifactGlobs []string `json:"artifact_globs"`
	Trigger       string   `json:"trigger"`
}

func (a *API) createReleaseRun(c iris.Context) {
	var in releaseRunRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		a.badRequest(c, "project_id 必填")
		return
	}
	if strings.TrimSpace(in.Version) == "" {
		a.badRequest(c, "version 必填")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 60*time.Minute)
	defer cancel()
	project, err := a.store.GetProject(ctx, in.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	record, run, err := a.executeReleaseRun(ctx, project, in)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_failed", Message: err.Error()}})
		return
	}
	pid := project.ID
	a.audit(ctx, c, "release.run", "release_run", strconv.FormatInt(record.ID, 10), &pid, run.Status,
		map[string]any{"version": run.Version, "commit": run.Commit, "candidates": len(run.Candidates),
			"release_url": run.ReleaseURL, "notify_status": run.NotifyStatus})
	_ = c.JSON(envelope{Data: iris.Map{"run": run, "record": record}})
}

func (a *API) rollbackReleaseRun(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.badRequest(c, "非法 id")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Minute)
	defer cancel()
	record, err := a.store.GetReleaseRun(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	if record.ProjectID == nil {
		a.badRequest(c, "该记录没有关联项目")
		return
	}
	project, err := a.store.GetProject(ctx, *record.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	client, err := bridge.ClientFromCredentials(ctx, a.store, &project.ID)
	if err != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "github_unavailable", Message: err.Error()}})
		return
	}
	var candidates []release.Candidate
	_ = json.Unmarshal(record.Candidates, &candidates)
	ready, released := a.releaseLabels(project, "")
	run := &release.Run{
		ID: record.ID, ProjectID: project.ID, Version: record.Version, Status: record.Status,
		Candidates: candidates, ReleaseURL: record.ReleaseURL, Tag: record.Tag, ReleaseID: record.ReleaseID,
	}
	rollErr := release.Rollback(ctx, run, repo, client, a.releaseNotifier(), ready, released)
	if rollErr != nil {
		a.audit(ctx, c, "release.rollback", "release_run", strconv.FormatInt(record.ID, 10), &project.ID, "failed",
			map[string]any{"version": record.Version, "error": rollErr.Error()})
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "rollback_failed", Message: rollErr.Error()}})
		return
	}
	record.Status = "rolled_back"
	note := "回滚：已删除 Release 与 tag，候选 Issue 恢复待发版"
	if record.Error != "" {
		note = record.Error + "; " + note
	}
	record.Error = note
	if err := a.store.UpdateReleaseRun(ctx, *record); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "release.rollback", "release_run", strconv.FormatInt(record.ID, 10), &project.ID, "ok",
		map[string]any{"version": record.Version, "tag": record.Tag})
	_ = c.JSON(envelope{Data: record})
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
