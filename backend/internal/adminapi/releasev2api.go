package adminapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/releaseagent"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

func (a *API) registerReleaseV2(p iris.Party) {
	p.Get("/v2/plans/{id}", a.getReleaseV2Plan)
	p.Post("/v2/plans", a.createReleaseV2Plan)
	p.Get("/v2/runs", a.listReleaseV2Runs)
	p.Post("/v2/runs", a.createReleaseV2Run)
	p.Get("/v2/runs/{id}", a.getReleaseV2Run)
	p.Get("/v2/runs/{id}/events", a.listReleaseV2Events)
	p.Post("/v2/runs/{id}/cancel", a.cancelReleaseV2Run)
	p.Post("/v2/runs/{id}/agent-token", a.issueReleaseV2AgentToken)
	p.Get("/v2/runs/{id}/approvals", a.listReleaseV2Approvals)
	p.Post("/v2/runs/{id}/approvals", a.createReleaseV2Approval)
	a.registerReleaseV2Agent(p)
	p.Post("/v2/runs/{id}/events/ticket", a.issueReleaseV2EventTicket)
	p.Get("/v2/settings", a.getReleaseV2Settings)
	p.Put("/v2/settings", a.putReleaseV2Settings)
	p.Post("/v2/settings/probe", a.probeReleaseV2Settings)
	p.Get("/v2/executors", a.listReleaseV2Executors)
	p.Post("/v2/executors", a.upsertReleaseV2Executor)
	p.Get("/v2/executors/bootstrap", a.getReleaseV2ExecutorBootstrap)
}

type releaseV2PlanRequest struct {
	ProjectID        string                     `json:"project_id"`
	Version          string                     `json:"version"`
	BuildVersion     string                     `json:"build_version"`
	Channel          string                     `json:"channel"`
	Mode             string                     `json:"mode"`
	Branch           string                     `json:"branch"`
	Commit           string                     `json:"commit"`
	Targets          []string                   `json:"targets"`
	ExecutorBindings map[string]string          `json:"executor_bindings"`
	Candidates       []releasev2.CandidateIssue `json:"candidate_issues"`
	Notes            string                     `json:"notes"`
	TTLSeconds       int                        `json:"ttl_seconds"`
}

func (a *API) createReleaseV2Plan(c iris.Context) {
	var in releaseV2PlanRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if in.ProjectID == "" || strings.TrimSpace(in.Version) == "" {
		a.badRequest(c, "project_id 与 version 必填")
		return
	}
	ctx := c.Request().Context()
	project, err := a.store.GetProject(ctx, in.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = firstNonEmpty(project.GitHubDefaultBranch, "main")
	}
	profilePath, profile, err := releaseV2ProfilePath(project)
	if err != nil {
		c.StatusCode(http.StatusUnprocessableEntity)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_profile_missing", Message: err.Error()}})
		return
	}
	profileHash, err := profile.ProfileSHA256()
	if err != nil {
		a.fail(c, err)
		return
	}
	targets, err := profile.PlanTargets(in.ExecutorBindings, in.Targets)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	blockers := []releasev2.Blocker{}
	if repo == "" {
		blockers = append(blockers, releasev2.Blocker{Code: "repo_missing", Message: "项目未配置 GitHub 仓库"})
	}
	online := map[string]bool{}
	byPlatform := map[string][]string{}
	if executors, lerr := a.store.ListExecutors(ctx); lerr == nil {
		for _, executor := range executors {
			if !strings.EqualFold(executor.Status, "online") {
				continue
			}
			online[executor.ID] = true
			key := strings.ToLower(executor.OS) + "/" + strings.ToLower(executor.Arch)
			byPlatform[key] = append(byPlatform[key], executor.ID)
		}
	}
	for i := range targets {
		if targets[i].ExecutorID == "" {
			candidates := append([]string(nil), byPlatform[targets[i].OS+"/"+targets[i].Arch]...)
			sort.Strings(candidates)
			if len(candidates) > 0 {
				targets[i].ExecutorID = candidates[0]
				continue
			}
			blockers = append(blockers, releasev2.Blocker{Code: "executor_required", TargetID: targets[i].ID, Message: "没有在线的 " + targets[i].OS + "/" + targets[i].Arch + " 构建机"})
			continue
		}
		if !online[targets[i].ExecutorID] {
			blockers = append(blockers, releasev2.Blocker{Code: "executor_offline", TargetID: targets[i].ID, Message: "构建机不在线：" + targets[i].ExecutorID})
		}
	}
	commit := strings.TrimSpace(in.Commit)
	if commit == "" && repo != "" {
		client, cerr := bridge.ClientForRepo(ctx, a.store, &project.ID, repo)
		if cerr != nil {
			blockers = append(blockers, releasev2.Blocker{Code: "github_unavailable", Message: cerr.Error()})
		} else if sha, serr := client.BranchSHA(ctx, repo, branch); serr != nil {
			blockers = append(blockers, releasev2.Blocker{Code: "commit_unresolved", Message: serr.Error()})
		} else {
			commit = sha
		}
	}
	mode := releasev2.Mode(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = releasev2.ModePublish
	}
	ttl := time.Duration(in.TTLSeconds) * time.Second
	plan, err := a.releaseV2.CreatePlan(ctx, releasev2.PlanRequest{
		ProjectID: project.ID, RequestedBy: a.actor(c), Trigger: "manual", Mode: mode,
		Version: in.Version, BuildVersion: in.BuildVersion, Channel: in.Channel, Repo: repo,
		Branch: branch, Commit: commit, ResolvedCommit: commit, ProfileSHA256: profileHash,
		CloneCredential: cloneCredentialOf(profile),
		Candidates:      in.Candidates, Targets: targets, Blockers: blockers, NotesMD: in.Notes,
		TTL: ttl,
	})
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	a.audit(ctx, c, "release.v2.plan", "release_plan_v2", plan.ID, &project.ID, "ok", map[string]any{
		"version": plan.Version, "commit": plan.ResolvedCommit, "targets": len(plan.Targets), "blockers": len(plan.Blockers), "profile": profilePath,
	})
	_ = c.JSON(envelope{Data: plan})
}

func (a *API) getReleaseV2Plan(c iris.Context) {
	plan, err := a.releaseV2.GetPlan(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	_ = c.JSON(envelope{Data: plan})
}

type releaseV2RunRequest struct {
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a *API) createReleaseV2Run(c iris.Context) {
	var in releaseV2RunRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.PlanID) == "" {
		a.badRequest(c, "plan_id 必填")
		return
	}
	run, err := a.releaseV2.CreateRun(c.Request().Context(), in.PlanID, in.IdempotencyKey, a.actor(c))
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	pid := run.ProjectID
	a.audit(c.Request().Context(), c, "release.v2.run", "release_run_v2", run.ID, &pid, "ok", map[string]any{
		"plan_id": run.PlanID, "version": run.Version, "mode": run.Mode,
	})
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: run})
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

func releaseV2ProfilePath(project *store.Project) (string, *releasev2.Profile, error) {
	path := strings.TrimSpace(project.ReleaseProfile)
	if path == "" {
		path = "smartpipe.yaml"
	}
	if !filepath.IsAbs(path) {
		base := strings.TrimSpace(project.LocalRepoPath)
		if base == "" {
			return path, nil, errors.New("项目未配置本地仓库目录，无法读取发版配置")
		}
		path = filepath.Join(base, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return path, nil, errors.New("发版配置不可读取：" + path)
	}
	profile, err := releasev2.LoadProfile(path)
	if err != nil {
		return path, nil, err
	}
	return path, profile, nil
}

func sortedExecutorIDs(items []releasev2.Executor) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

func (a *API) listReleaseV2Approvals(c iris.Context) {
	items, err := a.store.ListApprovals(c.Request().Context(), c.Params().Get("id"), 20)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) createReleaseV2Approval(c iris.Context) {
	var in struct {
		Reason     string `json:"reason"`
		TTLSeconds int    `json:"ttl_seconds"`
		// AutoResume 为 false 时只记录批准，不自动拉起发布 Agent（脚本/CI 手动推进时用）。
		AutoResume *bool `json:"auto_resume"`
	}
	_ = decodeBody(c, &in)
	ctx := c.Request().Context()
	run, err := a.releaseV2.GetRun(ctx, c.Params().Get("id"))
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	content := run.PlanSHA256
	if pubs, perr := a.store.ListPublications(ctx, run.ID); perr == nil {
		for _, pub := range pubs {
			if pub.Provider == "update_service" && strings.TrimSpace(pub.RequestSHA256) != "" {
				content = pub.RequestSHA256
			}
		}
	}
	ttl := time.Duration(in.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	expires := time.Now().Add(ttl)
	approval, err := a.store.CreateApproval(ctx, releasev2.Approval{
		RunID: run.ID, Kind: "publish", Actor: a.actor(c), Reason: strings.TrimSpace(in.Reason),
		ContentSHA256: content, State: "approved", ExpiresAt: &expires,
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	pid := run.ProjectID
	a.audit(ctx, c, "release.v2.approve", "release_run_v2", run.ID, &pid, "ok", map[string]any{
		"approval_id": approval.ID, "content_sha256": content, "expires_at": expires,
	})
	if in.AutoResume == nil || *in.AutoResume {
		a.resumeReleaseV2AgentAfterApproval(ctx, c, run)
	}
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: approval})
}

// resumeReleaseV2AgentAfterApproval 在批准后自动把发布 Agent 接回去继续收口，
// 省掉“批准完还要再点一次启动”。关闭方式：SMARTPIPE_RELEASE_AGENT_AUTO_RESUME=off。
func (a *API) resumeReleaseV2AgentAfterApproval(ctx context.Context, c iris.Context, run *releasev2.Run) {
	if a.releaseAgent == nil || !a.releaseAgent.Enabled() || a.autoResumeAgentOff {
		return
	}
	switch run.Status {
	case releasev2.RunStaged, releasev2.RunPublishing, releasev2.RunPartiallyPublished:
	default:
		return
	}
	if a.releaseAgent.Running(run.ID) {
		return
	}
	bundle, err := a.releaseV2.GetRunBundle(ctx, run.ID, 0, 1)
	if err != nil {
		a.audit(ctx, c, "release.v2.agent.auto_resume", "release_run_v2", run.ID, &run.ProjectID, "error", map[string]any{"error": err.Error()})
		return
	}
	session, err := a.releaseAgent.Launch(ctx, bundle, releaseagent.Options{Actor: "system:approval"})
	if err != nil {
		if errors.Is(err, releaseagent.ErrLimit) {
			a.audit(ctx, c, "release.v2.agent.auto_resume", "release_run_v2", run.ID, &run.ProjectID, "skipped", map[string]any{"reason": "agent_limit"})
			return
		}
		a.audit(ctx, c, "release.v2.agent.auto_resume", "release_run_v2", run.ID, &run.ProjectID, "error", map[string]any{"error": err.Error()})
		return
	}
	a.audit(ctx, c, "release.v2.agent.auto_resume", "release_run_v2", run.ID, &run.ProjectID, "ok", map[string]any{"session_id": session.ID})
}

// cloneCredentialOf 从发版配置里取私有仓库凭据引用；没有就返回空，执行器按公开仓库处理。
func cloneCredentialOf(profile *releasev2.Profile) string {
	if profile == nil || profile.Release == nil {
		return ""
	}
	return strings.TrimSpace(profile.Release.Source.Credential)
}
