package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

func (a *API) registerReleaseV2(p iris.Party) {
	p.Get("/v2/projects/{id}/release-config", a.getReleaseV2ProjectConfig)
	p.Put("/v2/projects/{id}/release-config", a.putReleaseV2ProjectConfig)
	p.Get("/v2/projects/{id}/release-preview", a.getReleaseV2ProjectPreview)
	p.Get("/v2/runs", a.listReleaseV2Runs)
	p.Post("/v2/runs", a.createReleaseV2Run)
	p.Get("/v2/runs/{id}", a.getReleaseV2Run)
	p.Get("/v2/runs/{id}/events", a.listReleaseV2Events)
	p.Post("/v2/runs/{id}/cancel", a.cancelReleaseV2Run)
	p.Post("/v2/runs/{id}/agent-token", a.issueReleaseV2AgentToken)
	a.registerReleaseV2Agent(p)
	p.Post("/v2/runs/{id}/events/ticket", a.issueReleaseV2EventTicket)
	p.Get("/v2/settings", a.getReleaseV2Settings)
	p.Put("/v2/settings", a.putReleaseV2Settings)
	p.Post("/v2/settings/probe", a.probeReleaseV2Settings)
	p.Get("/v2/executors", a.listReleaseV2Executors)
	p.Post("/v2/executors", a.upsertReleaseV2Executor)
	p.Get("/v2/executors/bootstrap", a.getReleaseV2ExecutorBootstrap)
	p.Post("/v2/executors/deploy", a.deployReleaseV2Executor)
	p.Delete("/v2/executors/{id}", a.removeReleaseV2Executor)
}

type releaseV2RunRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	// 新的三输入入口：项目、升级服务、构建机。
	ProjectID       string   `json:"project_id"`
	ExecutorIDs     []string `json:"executor_ids"`
	UpdateServerIDs []string `json:"update_server_ids"`
	// Mode 只用于构建演练（build_only）：默认 publish，由 Agent 一路发到渠道。
	Mode string `json:"mode,omitempty"`
}

// createReleaseV2Run 只有一个入口：项目 + 升级服务器 + 构建机。版本、tag、
// commit、构建命令全部从项目配置和仓库派生。
func (a *API) createReleaseV2Run(c iris.Context) {
	var in releaseV2RunRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		a.badRequest(c, "project_id 必填")
		return
	}
	a.createReleaseV2RunFromIntake(c, in)
}

func (a *API) listReleaseV2Runs(c iris.Context) {
	items, err := a.releaseV2.ListRuns(c.Request().Context(), c.URLParam("project_id"), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getReleaseV2Run(c iris.Context) {
	after, _ := strconv.ParseInt(c.URLParam("after"), 10, 64)
	item, err := a.releaseV2.GetRunBundle(c.Request().Context(), c.Params().Get("id"), after, intQuery(c, "event_limit", 500))
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	_ = c.JSON(envelope{Data: item})
}

func (a *API) listReleaseV2Events(c iris.Context) {
	after, _ := strconv.ParseInt(c.URLParam("after"), 10, 64)
	runID := c.Params().Get("id")
	if _, err := a.releaseV2.GetRun(c.Request().Context(), runID); err != nil {
		a.releaseV2Error(c, err)
		return
	}
	items, err := a.store.ListEvents(c.Request().Context(), runID, after, intQuery(c, "limit", 500))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) cancelReleaseV2Run(c iris.Context) {
	var in struct {
		Reason string `json:"reason"`
	}
	_ = decodeBody(c, &in)
	run, err := a.releaseV2.CancelRun(c.Request().Context(), c.Params().Get("id"), a.actor(c), in.Reason)
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	if a.releaseAgent.Cancel(run.ID) {
		a.audit(c.Request().Context(), c, "release.v2.agent.cancel", "release_run_v2", run.ID, &run.ProjectID, "ok", nil)
	}
	pid := run.ProjectID
	a.audit(c.Request().Context(), c, "release.v2.cancel", "release_run_v2", run.ID, &pid, "ok", map[string]any{"reason": in.Reason})
	_ = c.JSON(envelope{Data: run})
}

func (a *API) listReleaseV2Executors(c iris.Context) {
	items, err := a.store.ListExecutors(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) upsertReleaseV2Executor(c iris.Context) {
	var in releasev2.Executor
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	in.OS = strings.ToLower(strings.TrimSpace(in.OS))
	in.Arch = strings.ToLower(strings.TrimSpace(in.Arch))
	if in.ID == "" || in.OS == "" || in.Arch == "" {
		a.badRequest(c, "id、os、arch 必填")
		return
	}
	if in.Status == "" {
		in.Status = "online"
	}
	if in.Labels == nil {
		in.Labels = []string{}
	}
	if in.Capabilities == nil {
		in.Capabilities = map[string]any{}
	}
	if err := a.store.UpsertExecutor(c.Request().Context(), in); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "release.v2.executor.upsert", "release_executor", in.ID, nil, "ok", map[string]any{
		"os": in.OS, "arch": in.Arch, "status": in.Status,
	})
	_ = c.JSON(envelope{Data: in})
}

func (a *API) issueReleaseV2AgentToken(c iris.Context) {
	var in struct {
		Scopes     []string `json:"scopes"`
		TTLSeconds int      `json:"ttl_seconds"`
	}
	_ = decodeBody(c, &in)
	run, err := a.releaseV2.GetRun(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"release.read", "executor.read", "artifact.read"}
	}
	ttl := time.Duration(in.TTLSeconds) * time.Second
	raw, token, err := releasev2.NewAgentToken(run.ID, run.ProjectID, in.Scopes, ttl, time.Now())
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	if err := a.store.CreateReleaseV2Token(c.Request().Context(), token); err != nil {
		a.fail(c, err)
		return
	}
	pid := run.ProjectID
	a.audit(c.Request().Context(), c, "release.v2.agent_token", "release_run_v2", run.ID, &pid, "ok", map[string]any{
		"token_id": token.ID, "scopes": token.Scopes, "expires_at": token.ExpiresAt,
	})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{
		"token": raw, "token_id": token.ID, "expires_at": token.ExpiresAt, "scopes": token.Scopes,
	}})
}
func (a *API) releaseV2Error(c iris.Context, err error) {
	switch {
	case errors.Is(err, releasev2.ErrInvalid):
		c.StatusCode(http.StatusBadRequest)
		_ = c.JSON(envelope{Err: &apiError{Code: "invalid_argument", Message: err.Error()}})
	case errors.Is(err, releasev2.ErrPlanExpired):
		c.StatusCode(http.StatusGone)
		_ = c.JSON(envelope{Err: &apiError{Code: "plan_expired", Message: err.Error()}})
	case errors.Is(err, releasev2.ErrPlanBlocked), errors.Is(err, releasev2.ErrState), errors.Is(err, releasev2.ErrIdempotency), errors.Is(err, store.ErrConflict):
		c.StatusCode(http.StatusConflict)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_blocked", Message: err.Error()}})
	default:
		a.fail(c, err)
	}
}

// releaseProfileFallbacks 是项目没显式配置「发版配置路径」时依次尝试的位置。
// 仓库根的 smartpipe.yaml 最常见，其余是分开放的约定位置。
var releaseProfileFallbacks = []string{
	"smartpipe.yaml",
	".smartpipe/release.yaml",
	".smartpipe/release.yml",
	"deploy/smartpipe.yaml",
	"release/smartpipe.yaml",
}

// resolveReleaseProfile 读取项目的发版配置。
//
// 读之前先尽力把本地 clone 拉到远端：发版配置和构建命令都随代码走，镜像停在
// 克隆那天会让人对着旧文件排查（比如仓库里已经有 release.targets，本地却只有
// 验证契约）。同步失败不阻塞计划，读取本地现有文件并在错误里带上原因。
func (a *API) resolveReleaseProfile(ctx context.Context, project *store.Project, branch string) (string, *releasev2.Profile, error) {
	base := strings.TrimSpace(project.LocalRepoPath)
	configured := strings.TrimSpace(project.ReleaseProfile)
	note := ""
	if configured == "" || !filepath.IsAbs(configured) {
		note = a.refreshProjectClone(ctx, project, branch)
	}
	if configured != "" {
		path := configured
		if !filepath.IsAbs(path) {
			if base == "" {
				return path, nil, errors.New("项目未配置本地仓库目录，无法读取发版配置")
			}
			path = filepath.Join(base, path)
		}
		return readReleaseProfile(path, note)
	}
	if base == "" {
		return "smartpipe.yaml", nil, errors.New("项目未配置本地仓库目录，无法读取发版配置")
	}
	for _, rel := range releaseProfileFallbacks {
		path := filepath.Join(base, rel)
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			continue
		}
		return readReleaseProfile(path, note)
	}
	return filepath.Join(base, releaseProfileFallbacks[0]), nil, fmt.Errorf(
		"仓库里没有发版配置：%s 下试过 %s。请在仓库里加一份含 release.targets 的 smartpipe.yaml，或在项目设置的「发版配置路径」里指定已有文件%s",
		base, strings.Join(releaseProfileFallbacks, "、"), withProfileNote(note))
}

func readReleaseProfile(path, note string) (string, *releasev2.Profile, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return path, nil, fmt.Errorf("发版配置不可读取：%s%s", path, withProfileNote(note))
	}
	profile, err := releasev2.LoadProfile(path)
	switch {
	case err == nil:
		return path, profile, nil
	case errors.Is(err, releasev2.ErrNoReleaseProfile):
		return path, nil, fmt.Errorf(
			"发版配置 %s 里没有 release.targets：它可能只写了验证契约。请补上发版目标（构建/校验命令与制品），或把「发版配置路径」指向含 release.targets 的文件%s",
			path, withProfileNote(note))
	case errors.Is(err, releasev2.ErrInvalid):
		return path, nil, fmt.Errorf("发版配置 %s 不合法：%v%s", path, strings.TrimPrefix(err.Error(), "releasev2: "), withProfileNote(note))
	default:
		return path, nil, fmt.Errorf("读取发版配置 %s 失败：%v%s", path, err, withProfileNote(note))
	}
}

// refreshProjectClone 在生成计划前把本地镜像快进到远端分支，返回一句可附在错误里的说明。
func (a *API) refreshProjectClone(ctx context.Context, project *store.Project, branch string) string {
	dir := strings.TrimSpace(project.LocalRepoPath)
	remote := strings.TrimSpace(project.GitHubRepoURL)
	if dir == "" || remote == "" {
		return ""
	}
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		return ""
	}
	token := ""
	if tok, err := gitToken(ctx, a.store, &project.ID); err == nil {
		token = tok
	}
	if strings.TrimSpace(branch) == "" {
		branch = firstNonEmpty(project.GitHubDefaultBranch, "main")
	}
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := syncLocalClone(sctx, dir, remote, token, branch)
	if err != nil {
		return "本地仓库同步失败，按现有文件判断：" + unwrapGitOutput(err).Error()
	}
	if res.Updated {
		head := res.Head
		if len(head) > 8 {
			head = head[:8]
		}
		return "已先把本地仓库快进到 " + head
	}
	return ""
}

func withProfileNote(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return "（" + note + "）"
}

func sortedExecutorIDs(items []releasev2.Executor) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

// cloneCredentialOf 从发版配置里取私有仓库凭据引用；没有就返回空，执行器按公开仓库处理。
func cloneCredentialOf(profile *releasev2.Profile) string {
	if profile == nil || profile.Release == nil {
		return ""
	}
	return strings.TrimSpace(profile.Release.Source.Credential)
}
