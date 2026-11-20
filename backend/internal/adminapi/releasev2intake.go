package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/releaseagent"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

type releaseV2PreviewBuild struct {
	TargetID   string   `json:"target_id"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	HostOS     string   `json:"host_os,omitempty"`
	HostArch   string   `json:"host_arch,omitempty"`
	ExecutorID string   `json:"executor_id,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Ready      bool     `json:"ready"`
	Message    string   `json:"message,omitempty"`
}

type releaseV2PreviewUpdateServer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

type releaseV2Preview struct {
	ProjectID string `json:"project_id"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Version   string `json:"version"`
	// VersionPending 表示用户指定了版本，创建发布会先写版本文件、提交主干并打 tag。
	VersionPending bool                           `json:"version_pending,omitempty"`
	Tag            string                         `json:"tag"`
	Commit         string                         `json:"commit"`
	CommitDate     string                         `json:"commit_date,omitempty"`
	BuildVersion   string                         `json:"build_version"`
	Notes          string                         `json:"notes,omitempty"`
	NotesPath      string                         `json:"notes_path,omitempty"`
	Builds         []releaseV2PreviewBuild        `json:"builds"`
	UpdateServers  []releaseV2PreviewUpdateServer `json:"update_servers"`
	Blockers       []releasev2.Blocker            `json:"blockers"`
	Warnings       []releaseVersionWarning        `json:"warnings,omitempty"`
}

func (a *API) getReleaseV2ProjectPreview(c iris.Context) {
	projectID := strings.TrimSpace(c.Params().Get("id"))
	project, err := a.store.GetProject(c.Request().Context(), projectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	preview, err := a.resolveReleasePreview(c.Request().Context(), project, releasePreviewOptions{Version: c.URLParam("version")})
	if err != nil {
		c.StatusCode(http.StatusUnprocessableEntity)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_preview_failed", Message: err.Error()}})
		return
	}
	// 预览阶段就把平台绑定到构建机，用户点开页面即可看到实际会用的机器。
	if _, blockers := a.planIntakeExecutors(c.Request().Context(), preview, nil); len(blockers) > 0 {
		preview.Blockers = append(preview.Blockers, blockers...)
	}
	// 预览阶段顺手查一下这个版本是不是已经在升级服务上发过了：构建要几十分钟，
	// 让操作者在按下按钮前就知道「同版本重发大概率会被 version_already_published 拦下」。
	if cfg, cfgErr := a.store.GetProjectReleaseConfig(c.Request().Context(), project.ID); cfgErr == nil {
		probe := a.cachedPublishedVersion(c.Request().Context(), cfg, preview.Version)
		preview.Warnings = append(preview.Warnings, versionWarnings(probe, preview.Version)...)
	}
	_ = c.JSON(envelope{Data: preview})
}

func (a *API) getReleaseV2ProjectConfig(c iris.Context) {
	cfg, err := a.store.GetProjectReleaseConfig(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: cfg})
}

func (a *API) putReleaseV2ProjectConfig(c iris.Context) {
	var cfg releasev2.ProjectReleaseConfig
	if err := decodeBody(c, &cfg); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	cfg.ProjectID = strings.TrimSpace(c.Params().Get("id"))
	if err := a.store.UpsertProjectReleaseConfig(c.Request().Context(), &cfg); err != nil {
		a.releaseV2Error(c, err)
		return
	}
	pid := cfg.ProjectID
	a.audit(c.Request().Context(), c, "release.v2.project_config", "project", pid, &pid, "ok", map[string]any{
		"builds": len(cfg.Builds), "channels": cfg.ChannelOrder,
	})
	_ = c.JSON(envelope{Data: cfg})
}

// createReleaseV2RunFromIntake 是新的三输入入口：项目、升级服务、构建机。
// 版本、tag、commit、说明与构建命令都从项目配置和仓库派生。
func (a *API) createReleaseV2RunFromIntake(c iris.Context, in releaseV2RunRequest) {
	ctx := c.Request().Context()
	project, err := a.store.GetProject(ctx, strings.TrimSpace(in.ProjectID))
	if err != nil {
		a.fail(c, err)
		return
	}
	cfg, err := a.store.GetProjectReleaseConfig(ctx, project.ID)
	if err != nil {
		a.fail(c, err)
		return
	}
	preview, err := a.resolveReleasePreview(ctx, project, releasePreviewOptions{Version: in.Version})
	if err != nil {
		c.StatusCode(http.StatusUnprocessableEntity)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_preview_failed", Message: err.Error()}})
		return
	}
	executorMap, blockers := a.planIntakeExecutors(ctx, preview, in.ExecutorIDs)
	preview.Blockers = append(preview.Blockers, blockers...)
	if len(preview.Blockers) > 0 {
		c.StatusCode(http.StatusUnprocessableEntity)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_blocked", Message: blockersMessage(preview.Blockers)}})
		return
	}
	// 拦截项都通过后才动仓库：界面上指定了版本时，先把版本写进版本文件、提交主干并打 tag，
	// 后续构建固定在这个提交上，保证产物版本号和仓库一致。
	var prep *releaseVersionPrep
	if strings.TrimSpace(in.Version) != "" {
		prep, err = a.prepareReleaseVersion(ctx, project, cfg, in.Version)
		if err != nil {
			c.StatusCode(http.StatusUnprocessableEntity)
			_ = c.JSON(envelope{Err: &apiError{Code: "version_prepare_failed", Message: err.Error()}})
			return
		}
		preview, err = a.resolveReleasePreview(ctx, project, releasePreviewOptions{Version: in.Version, Commit: prep.Commit})
		if err != nil {
			c.StatusCode(http.StatusUnprocessableEntity)
			_ = c.JSON(envelope{Err: &apiError{Code: "release_preview_failed", Message: err.Error()}})
			return
		}
	}
	channels := cfg.ChannelOrder
	if len(channels) == 0 {
		channels = []string{"beta", "stable"}
	}
	targets := make([]releasev2.TargetPlan, 0, len(preview.Builds))
	for _, build := range preview.Builds {
		recipe := cfg.Builds[build.TargetID]
		targets = append(targets, releasev2.TargetPlan{
			ID: build.TargetID, OS: build.OS, Arch: build.Arch,
			ExecutorID: executorMap[build.TargetID], Required: true,
			BuildCommand: recipe.Build, SignCommand: recipe.Sign, VerifyCommand: recipe.Verify,
			Artifacts: append([]releasev2.ArtifactRule(nil), recipe.Artifacts...),
		})
	}
	profileHash, err := releasev2.HashJSON(cfg)
	if err != nil {
		a.fail(c, err)
		return
	}
	mode := releasev2.ModePublish
	if strings.EqualFold(strings.TrimSpace(in.Mode), string(releasev2.ModeBuildOnly)) {
		mode = releasev2.ModeBuildOnly
	}
	plan, err := a.releaseV2.CreatePlan(ctx, releasev2.PlanRequest{
		ProjectID: project.ID, RequestedBy: a.actor(c), Trigger: "ui",
		Mode: mode, Version: preview.Version, BuildVersion: preview.BuildVersion,
		Tag: preview.Tag, Channel: channels[0], Repo: preview.Repo, Branch: preview.Branch,
		Commit: preview.Commit, ResolvedCommit: preview.Commit, ProfileSHA256: profileHash,
		Targets: targets, NotesMD: preview.Notes, TTL: 30 * time.Minute,
	})
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	run, err := a.releaseV2.CreateRun(ctx, plan.ID, in.IdempotencyKey, a.actor(c))
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	if prep != nil {
		branch := firstNonEmpty(project.GitHubDefaultBranch, "main")
		_ = a.store.AppendEvent(ctx, releasev2.Event{
			RunID: run.ID, Actor: a.actor(c), Kind: "release.version_bumped",
			Summary: fmt.Sprintf("%s 已写入 %s 并打 tag %s", prep.Version, branch, prep.Tag),
			Detail: map[string]any{
				"version": prep.Version, "tag": prep.Tag, "commit": prep.Commit,
				"files": prep.Files, "commits": prep.Commits, "branch": branch,
			},
		})
		a.audit(ctx, c, "release.v2.version_bump", "release_run_v2", run.ID, &project.ID, "ok", map[string]any{
			"version": prep.Version, "tag": prep.Tag, "commit": prep.Commit, "files": prep.Files,
		})
	}
	executorIDs := make([]string, 0, len(executorMap))
	seenExecutors := map[string]bool{}
	for _, id := range executorMap {
		if id == "" || seenExecutors[id] {
			continue
		}
		seenExecutors[id] = true
		executorIDs = append(executorIDs, id)
	}
	sort.Strings(executorIDs)
	serverIDs := in.UpdateServerIDs
	if len(serverIDs) == 0 {
		serverIDs = cfg.UpdateServerIDs
	}
	serverIDs, err = a.normalizeUpdateServers(ctx, serverIDs)
	if err != nil {
		c.StatusCode(http.StatusUnprocessableEntity)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_blocked", Message: err.Error()}})
		return
	}
	if err := a.store.UpdateRunIntake(ctx, run.ID, executorIDs, serverIDs); err != nil {
		a.fail(c, err)
		return
	}
	if err := a.freezeReleaseSource(ctx, run, project); err != nil {
		message := "源码冻结失败: " + err.Error()
		_, _ = a.releaseV2.CancelRun(ctx, run.ID, a.actor(c), message)
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "source_freeze_failed", Message: message}})
		return
	}
	if refreshed, getErr := a.releaseV2.GetRun(ctx, run.ID); getErr == nil {
		run = refreshed
	}
	pid := project.ID
	a.audit(ctx, c, "release.v2.intake", "release_run_v2", run.ID, &pid, "ok", map[string]any{
		"version": run.Version, "tag": run.Tag, "commit": run.ResolvedCommit, "mode": run.Mode,
		"executors": executorIDs, "update_servers": serverIDs,
	})
	if run.Mode != releasev2.ModeBuildOnly {
		a.launchReleaseAgentForRun(ctx, run.ID, project, "auto")
	}
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: run})
}

func (a *API) launchReleaseAgentForRun(ctx context.Context, runID string, project *store.Project, actor string) {
	if a.releaseAgent == nil || !a.releaseAgent.Enabled() {
		return
	}
	bundle, err := a.releaseV2.GetRunBundle(ctx, runID, 0, 1)
	if err != nil {
		return
	}
	opts := releaseagent.Options{Scopes: releasev2.ReleaseAgentScopes, Actor: actor}
	if project != nil {
		opts.Model = project.CodexModel
		opts.Effort = project.CodexReasoningEffort
	}
	_, _ = a.releaseAgent.Launch(ctx, bundle, opts)
}

func (a *API) planIntakeExecutors(ctx context.Context, preview *releaseV2Preview, selected []string) (map[string]string, []releasev2.Blocker) {
	executors, err := a.store.ListExecutors(ctx)
	if err != nil {
		return nil, []releasev2.Blocker{{Code: "executor_unavailable", Message: err.Error()}}
	}
	online := map[string]releasev2.Executor{}
	byPlatform := map[string][]string{}
	for _, executor := range executors {
		if !strings.EqualFold(executor.Status, "online") {
			continue
		}
		online[executor.ID] = executor
		key := strings.ToLower(executor.OS) + "/" + strings.ToLower(executor.Arch)
		byPlatform[key] = append(byPlatform[key], executor.ID)
	}
	for key := range byPlatform {
		sort.Strings(byPlatform[key])
	}
	selectedSet := map[string]bool{}
	for _, id := range selected {
		if id = strings.TrimSpace(id); id != "" {
			selectedSet[id] = true
		}
	}
	out := map[string]string{}
	blockers := []releasev2.Blocker{}
	used := map[string]bool{}
	for i := range preview.Builds {
		build := &preview.Builds[i]
		hostOS, hostArch := build.OS, build.Arch
		if build.HostOS != "" {
			hostOS, hostArch = build.HostOS, build.HostArch
		}
		platform := strings.ToLower(hostOS) + "/" + strings.ToLower(hostArch)
		build.Candidates = append([]string(nil), byPlatform[platform]...)
		// 优先挑还没被占用的构建机；同一个平台只需要一台时，允许顺序复用
		// （执行器一次只跑一个目标，跨平台构建常常由同一台机器串行完成）。
		pick := func(allowUsed bool) string {
			for _, id := range build.Candidates {
				if len(selectedSet) > 0 && !selectedSet[id] {
					continue
				}
				if !allowUsed && used[id] {
					continue
				}
				return id
			}
			return ""
		}
		chosen := pick(false)
		if chosen == "" {
			chosen = pick(true)
		}
		if chosen == "" {
			build.Ready = false
			build.Message = "没有可用的 " + platform + " 构建机（产物 " + build.TargetID + "）"
			blockers = append(blockers, releasev2.Blocker{Code: "executor_required", TargetID: build.TargetID, Message: build.Message})
			continue
		}
		build.Ready = true
		build.ExecutorID = chosen
		out[build.TargetID] = chosen
		used[chosen] = true
	}
	return out, blockers
}

func blockersMessage(blockers []releasev2.Blocker) string {
	parts := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		if blocker.TargetID != "" {
			parts = append(parts, blocker.TargetID+": "+blocker.Message)
		} else {
			parts = append(parts, blocker.Message)
		}
	}
	return strings.Join(parts, "；")
}

// releasePreviewOptions 描述界面传进来的版本意图。
type releasePreviewOptions struct {
	// Version 是用户指定的发布版本；非空时预览只展示计划，创建发布时才会写仓库。
	Version string
	// Commit 是版本已提交后要固定的提交，避免预览和建单之间又有人推代码。
	Commit string
}

func (a *API) resolveReleasePreview(ctx context.Context, project *store.Project, opts releasePreviewOptions) (*releaseV2Preview, error) {
	cfg, err := a.store.GetProjectReleaseConfig(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	branch := firstNonEmpty(project.GitHubDefaultBranch, "main")
	preview := &releaseV2Preview{
		ProjectID: project.ID, Repo: repo, Branch: branch,
		Builds: []releaseV2PreviewBuild{}, Blockers: []releasev2.Blocker{},
		UpdateServers: []releaseV2PreviewUpdateServer{},
	}
	client, clientErr := bridge.ClientForRepo(ctx, a.store, &project.ID, repo)
	if repo == "" {
		preview.Blockers = append(preview.Blockers, releasev2.Blocker{Code: "repo_missing", Message: "项目未配置 GitHub 仓库"})
	}
	var tags []github.TagInfo
	if clientErr == nil && client != nil {
		if items, terr := client.ListTags(ctx, repo, 100); terr == nil {
			tags = items
		}
	}
	requested := strings.TrimSpace(opts.Version)
	version := requested
	fromTag := false
	if version == "" {
		if cfg.VersionFile != "" {
			version = a.readVersionFile(ctx, client, project, cfg, branch)
		}
		if version == "" && len(tags) > 0 {
			version = strings.TrimPrefix(strings.TrimPrefix(tags[0].Name, "v"), "V")
			fromTag = true
		}
		if version == "" {
			version = a.readVersionFile(ctx, client, project, cfg, branch)
		}
		if version == "" {
			return nil, fmt.Errorf("无法确定产品版本：请在项目发布配置里填写 version_file，或在仓库里打 tag")
		}
	}
	version, err = releasev2.NormalizeVersion(version)
	if err != nil {
		return nil, err
	}
	preview.Version = version
	preview.Tag = "v" + version
	preview.VersionPending = requested != ""
	for _, tag := range tags {
		if strings.TrimPrefix(strings.TrimPrefix(tag.Name, "v"), "V") == version {
			preview.Tag = tag.Name
			// 只有版本本身来自 tag（没配版本文件）时才把提交钉在 tag 上；
			// 版本来自版本文件时必须用分支最新提交，否则会构建到旧代码。
			if opts.Commit == "" && fromTag {
				preview.Commit = tag.SHA
			}
			break
		}
	}
	if opts.Commit != "" {
		preview.Commit = opts.Commit
	}
	if preview.Commit == "" && client != nil && clientErr == nil {
		if sha, serr := client.BranchSHA(ctx, repo, branch); serr == nil {
			preview.Commit = sha
		}
	}
	commitDate := time.Time{}
	if preview.Commit != "" && client != nil && clientErr == nil {
		if info, cerr := client.GetCommitInfo(ctx, repo, preview.Commit); cerr == nil && info != nil {
			commitDate = info.Date
		}
	}
	if commitDate.IsZero() {
		commitDate = a.localCommitDate(project, preview.Commit)
	}
	if preview.Commit == "" {
		preview.Commit = a.localCommitSHA(project, preview.Tag)
	}
	if commitDate.IsZero() && preview.Commit != "" {
		commitDate = a.localCommitDate(project, preview.Commit)
	}
	if preview.Commit == "" {
		preview.Blockers = append(preview.Blockers, releasev2.Blocker{Code: "commit_unresolved", Message: "无法解析分支或 tag 对应的提交"})
	}
	preview.CommitDate = commitDate.UTC().Format("2006-01-02")
	preview.BuildVersion = renderReleaseTemplate(cfg.BuildVersionTemplate, preview, commitDate)
	preview.NotesPath = renderReleaseTemplate(firstNonEmpty(cfg.NotesPath, "docs/releases/{tag}.md"), preview, commitDate)
	preview.Notes = a.readReleaseNotes(ctx, client, project, preview.NotesPath, preview.Commit)
	// 仓库里没有手写发布说明时，用上一版 tag 到本次提交的改动自动生成更新日志：
	// 最近几个版本发布出来的 Release 正文为空，就是缺了这一步兜底。
	if strings.TrimSpace(preview.Notes) == "" && clientErr == nil {
		preview.Notes = a.generateReleaseChangelog(ctx, client, repo, previousReleaseTag(tags, preview.Tag), preview.Tag, preview.Commit)
	}
	for _, targetID := range cfg.TargetIDs() {
		osName, arch := splitIntakeTarget(targetID)
		build := releaseV2PreviewBuild{TargetID: targetID, OS: osName, Arch: arch}
		if recipe, ok := cfg.Builds[targetID]; ok {
			build.HostOS = recipe.HostOS
			build.HostArch = recipe.HostArch
		}
		preview.Builds = append(preview.Builds, build)
	}
	if len(preview.Builds) == 0 {
		preview.Blockers = append(preview.Blockers, releasev2.Blocker{Code: "builds_missing", Message: "项目还没配置构建配方"})
	}
	services, _ := a.configuredUpdateServices(ctx)
	for _, service := range services {
		if strings.TrimSpace(service.URL) == "" {
			continue
		}
		preview.UpdateServers = append(preview.UpdateServers, releaseV2PreviewUpdateServer{
			ID: service.Name, Name: service.Name, URL: service.URL, Enabled: service.Enabled,
		})
	}
	return preview, nil
}

// normalizeUpdateServers trims and dedupes the selection, and rejects names the
// release settings do not know about, so a typo fails before any build starts.
func (a *API) normalizeUpdateServers(ctx context.Context, wanted []string) ([]string, error) {
	services, err := a.configuredUpdateServices(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]string, len(services))
	for _, service := range services {
		name := strings.TrimSpace(service.Name)
		if name == "" || !service.Enabled || strings.TrimSpace(service.URL) == "" {
			continue
		}
		known[strings.ToLower(name)] = name
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(wanted))
	unknown := make([]string, 0)
	for _, raw := range wanted {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		canonical, ok := known[strings.ToLower(name)]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("升级服务没有配置或已停用：%s", strings.Join(unknown, "、"))
	}
	return out, nil
}

func (a *API) configuredUpdateServices(ctx context.Context) ([]releaseUpdateService, error) {
	raw, err := a.store.GetSetting(ctx, "release_update_services")
	if err == nil && strings.TrimSpace(raw) != "" {
		var services []releaseUpdateService
		if err := json.Unmarshal([]byte(raw), &services); err == nil && len(services) > 0 {
			return services, nil
		}
	}
	url, _ := a.store.GetSetting(ctx, "release_update_url")
	credential, _ := a.store.GetSetting(ctx, "release_update_credential")
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	return []releaseUpdateService{{Name: "默认", URL: url, Credential: credential, Enabled: true}}, nil
}

func (a *API) readVersionFile(ctx context.Context, client *github.Client, project *store.Project, cfg *releasev2.ProjectReleaseConfig, branch string) string {
	files := releaseVersionFiles(cfg)
	if len(files) == 0 {
		return ""
	}
	// 多文件配置时以第一个文件为准；写入时会把所有文件都改成同一个版本。
	path := files[0]
	if client != nil {
		if content, ok, err := client.ReadFile(ctx, project.GitHubRepoURL, branch, path); err == nil && ok {
			return versionFromJSON(content, cfg.VersionJSONPath)
		}
	}
	base := strings.TrimSpace(project.LocalRepoPath)
	if base == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return versionFromJSON(string(raw), cfg.VersionJSONPath)
}

func versionFromJSON(raw, jsonPath string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return ""
	}
	for _, part := range strings.Split(firstNonEmpty(jsonPath, "version"), ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[part]
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func (a *API) readReleaseNotes(ctx context.Context, client *github.Client, project *store.Project, path, commit string) string {
	if client != nil && commit != "" {
		if content, ok, err := client.ReadFile(ctx, project.GitHubRepoURL, commit, path); err == nil && ok {
			return content
		}
	}
	base := strings.TrimSpace(project.LocalRepoPath)
	if base == "" || path == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return string(raw)
}

// previousReleaseTag 返回当前版本之前的最近一个 tag。ListTags 由新到旧，
// 当前 tag 可能已经存在（同版本重发），所以先跳过它自己。
func previousReleaseTag(tags []github.TagInfo, currentTag string) string {
	current := strings.TrimSpace(currentTag)
	for _, tag := range tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" || strings.EqualFold(name, current) {
			continue
		}
		return name
	}
	return ""
}

// generateReleaseChangelog 读取两个 tag 之间的提交并渲染成更新日志。
// GitHub 不可用或没有历史 tag 时返回空串，不影响发布预览本身。
func (a *API) generateReleaseChangelog(ctx context.Context, client *github.Client, repo, previousTag, tag, commit string) string {
	previousTag = strings.TrimSpace(previousTag)
	commit = strings.TrimSpace(commit)
	if client == nil || previousTag == "" || commit == "" {
		return ""
	}
	commits, err := client.CommitsBetween(ctx, repo, previousTag, commit, 500)
	if err != nil || len(commits) == 0 {
		return ""
	}
	entries := make([]releasev2.ChangelogCommit, 0, len(commits))
	for _, commit := range commits {
		entries = append(entries, releasev2.ChangelogCommit{SHA: commit.SHA, Message: commit.Message})
	}
	return releasev2.RenderChangelog(previousTag, tag, githubCompareURL(repo, previousTag, tag), entries, 25)
}

func githubCompareURL(repo, base, head string) string {
	owner, name, err := github.SplitRepo(repo)
	if err != nil || owner == "" || name == "" || base == "" || head == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s", owner, name, base, head)
}

func (a *API) localCommitSHA(project *store.Project, ref string) string {
	if project == nil || strings.TrimSpace(project.LocalRepoPath) == "" || ref == "" {
		return ""
	}
	out, err := gitOutput(project.LocalRepoPath, "rev-parse", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (a *API) localCommitDate(project *store.Project, commit string) time.Time {
	if project == nil || strings.TrimSpace(project.LocalRepoPath) == "" || commit == "" {
		return time.Time{}
	}
	out, err := gitOutput(project.LocalRepoPath, "show", "-s", "--format=%cI", commit)
	if err != nil {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(out))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func splitIntakeTarget(id string) (string, string) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(id)), "-", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func renderReleaseTemplate(template string, preview *releaseV2Preview, commitDate time.Time) string {
	if preview == nil {
		return template
	}
	replacements := map[string]string{
		"{version}": preview.Version,
		"{tag}":     preview.Tag,
		"{commit}":  preview.Commit,
		"{date}":    commitDate.UTC().Format("2006-01-02"),
	}
	out := template
	for key, value := range replacements {
		out = strings.ReplaceAll(out, key, value)
	}
	return out
}
