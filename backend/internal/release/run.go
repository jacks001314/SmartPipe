package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"smartpipe/backend/internal/github"
)

// BuilderFunc 允许注入构建实现，生产代码默认使用 Build。
type BuilderFunc func(ctx context.Context, req Request, workRoot string) (*Report, error)

// Forge 是发版需要的 GitHub 能力子集，便于用假实现做测试。
type Forge interface {
	ListIssuesByLabel(ctx context.Context, repo, label string, limit int) ([]github.Issue, error)
	BranchSHA(ctx context.Context, repo, ref string) (string, error)
	TagSHA(ctx context.Context, repo, tag string) (string, error)
	CreateTagRef(ctx context.Context, repo, tag, sha string) error
	DeleteTagRef(ctx context.Context, repo, tag string) error
	CreateRelease(ctx context.Context, repo, tag, target, title, body string) (*github.Release, error)
	UploadReleaseAsset(ctx context.Context, repo string, releaseID int64, name string, data []byte) (*github.Asset, error)
	DeleteRelease(ctx context.Context, repo string, id int64) error
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
	AddLabels(ctx context.Context, repo string, number int, labels []string) error
	Comment(ctx context.Context, repo string, number int, body string) error
}

// Notifier 把发版结果通知到项目群（蓝信）。
type Notifier interface {
	Notify(ctx context.Context, projectID, text string) error
}

// Candidate 是一个待发版的 Issue。
type Candidate struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author string `json:"author"`
}

// RunRequest 是一次发版运行的输入。
type RunRequest struct {
	ProjectID     string   `json:"project_id"`
	Repo          string   `json:"repo"`
	// CloneURL 是带凭据的克隆地址（私有仓库用）；为空时退回 Repo。
	CloneURL string `json:"clone_url,omitempty"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	Branch        string   `json:"branch"`
	ReadyLabel    string   `json:"ready_label"`
	ReleasedLabel string   `json:"released_label"`
	ArtifactGlobs []string `json:"artifact_globs"`
	Notes         string   `json:"notes"`
	DryRun        bool     `json:"dry_run"`
	Trigger       string   `json:"trigger"`
}

// Run 是一次发版的完整结果。
type Run struct {
	ID             int64       `json:"id"`
	ProjectID      string      `json:"project_id"`
	Version        string      `json:"version"`
	Commit         string      `json:"commit"`
	Branch         string      `json:"branch"`
	Status         string      `json:"status"`
	Trigger        string      `json:"trigger"`
	Candidates     []Candidate `json:"candidates"`
	Notes          string      `json:"notes"`
	ReleaseURL     string      `json:"release_url"`
	Tag            string      `json:"tag"`
	ReleaseID      int64       `json:"release_id"`
	ManifestSHA256 string      `json:"manifest_sha256"`
	OutputDir      string      `json:"output_dir"`
	Artifacts      []Artifact  `json:"artifacts"`
	BuildOutput    string      `json:"build_output"`
	Error          string      `json:"error"`
	NotifyStatus   string      `json:"notify_status"`
	NotifyError    string      `json:"notify_error"`
	StartedAt      time.Time   `json:"started_at"`
	FinishedAt     time.Time   `json:"finished_at"`
	DurationMS     int64       `json:"duration_ms"`
}

// Defaults 补全标签与触发来源。
func (r *RunRequest) Defaults() {
	if strings.TrimSpace(r.ReadyLabel) == "" {
		r.ReadyLabel = "smartpipe:ready"
	}
	if strings.TrimSpace(r.ReleasedLabel) == "" {
		r.ReleasedLabel = "smartpipe:released"
	}
	if strings.TrimSpace(r.Trigger) == "" {
		r.Trigger = "manual"
	}
	if strings.TrimSpace(r.Branch) == "" {
		r.Branch = "main"
	}
}

// CollectCandidates 读取 ready 标签下的 Issue。
func CollectCandidates(ctx context.Context, forge Forge, repo, label string) ([]Candidate, error) {
	issues, err := forge.ListIssuesByLabel(ctx, repo, label, 100)
	if err != nil {
		return nil, fmt.Errorf("release: list candidates: %w", err)
	}
	out := make([]Candidate, 0, len(issues))
	for _, issue := range issues {
		author := ""
		if issue.User != nil {
			author = issue.User.Login
		}
		out = append(out, Candidate{Number: issue.Number, Title: strings.TrimSpace(issue.Title), URL: issue.HTMLURL, Author: author})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// BuildNote 生成 Release Note（Markdown）。
func BuildNote(version string, candidates []Candidate, artifacts []Artifact, extra string, now time.Time) string {
	var b strings.Builder
	b.WriteString("# " + version + "\n\n")
	b.WriteString("发布日期：" + now.Local().Format("2006-01-02 15:04") + "  \n")
	b.WriteString(fmt.Sprintf("候选 Issue：%d 个\n\n", len(candidates)))
	b.WriteString("## 变更\n\n")
	if len(candidates) == 0 {
		b.WriteString("- 无\n")
	}
	for _, c := range candidates {
		author := ""
		if c.Author != "" {
			author = " (@" + c.Author + ")"
		}
		if c.URL != "" {
			b.WriteString(fmt.Sprintf("- [#%d %s](%s)%s\n", c.Number, c.Title, c.URL, author))
		} else {
			b.WriteString(fmt.Sprintf("- #%d %s%s\n", c.Number, c.Title, author))
		}
	}
	if len(artifacts) > 0 {
		b.WriteString("\n## 制品\n\n")
		for _, a := range artifacts {
			b.WriteString(fmt.Sprintf("- `%s` · %d B · `%s`\n", a.Path, a.Size, a.SHA256))
		}
	}
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\n## 说明\n\n" + strings.TrimSpace(extra) + "\n")
	}
	return b.String()
}

// BuildNotifyText 生成蓝信通知文本。
func BuildNotifyText(version, releaseURL string, candidates []Candidate, artifacts []Artifact) string {
	var b strings.Builder
	b.WriteString("【SmartPipe 发版】" + version + " 已发布\n")
	b.WriteString(fmt.Sprintf("包含 %d 个 Issue", len(candidates)))
	if len(candidates) > 0 {
		parts := make([]string, 0, len(candidates))
		for _, c := range candidates {
			parts = append(parts, fmt.Sprintf("#%d", c.Number))
		}
		b.WriteString("：" + strings.Join(parts, "、"))
	}
	b.WriteString("\n")
	if len(artifacts) > 0 {
		b.WriteString("制品：" + artifacts[0].Path)
		if len(artifacts) > 1 {
			b.WriteString(fmt.Sprintf(" 等 %d 个", len(artifacts)))
		}
		b.WriteString("（sha256 " + shortSHA(artifacts[0].SHA256) + "）\n")
	}
	if releaseURL != "" {
		b.WriteString("Release：" + releaseURL + "\n")
	}
	b.WriteString("如发现异常，请在 GitHub Release 页面或管理台执行回滚。")
	return b.String()
}

func shortSHA(s string) string {
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// Execute 执行一次发版：候选集 → 构建 → tag/Release → 回写标签 → 通知。
func Execute(ctx context.Context, req RunRequest, forge Forge, notify Notifier, build BuilderFunc, workRoot string) (*Run, error) {
	req.Defaults()
	started := time.Now().UTC()
	run := &Run{
		ProjectID: req.ProjectID, Version: strings.TrimSpace(req.Version), Branch: req.Branch,
		Trigger: req.Trigger, StartedAt: started, Status: "running",
	}
	if run.Version == "" || !versionPattern.MatchString(run.Version) {
		run.Status = "failed"
		run.Error = fmt.Sprintf("release: unsafe version %q", run.Version)
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, fmt.Errorf("%w: unsafe version %q", ErrInvalidRequest, run.Version)
	}
	if strings.TrimSpace(req.Repo) == "" {
		run.Status = "failed"
		run.Error = "release: repo is required"
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, fmt.Errorf("%w: repo is required", ErrInvalidRequest)
	}
	candidates, err := CollectCandidates(ctx, forge, req.Repo, req.ReadyLabel)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	run.Candidates = candidates
	notes := BuildNote(run.Version, candidates, nil, req.Notes, time.Now())
	run.Notes = notes
	if len(candidates) == 0 {
		run.Status = "skipped"
		run.Error = "release: no candidate issues labeled " + req.ReadyLabel
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	commit := strings.TrimSpace(req.Commit)
	if commit == "" {
		sha, err := forge.BranchSHA(ctx, req.Repo, req.Branch)
		if err != nil {
			run.Status = "failed"
			run.Error = "release: resolve branch head: " + err.Error()
			run.FinishedAt = time.Now().UTC()
			run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
			return run, nil
		}
		commit = sha
	}
	run.Commit = commit
	if build == nil {
		build = Build
	}
	report, err := build(ctx, Request{
		Repo: req.Repo, CloneURL: req.CloneURL, Commit: commit, Version: run.Version,
		ArtifactGlobs: req.ArtifactGlobs, Notes: notes,
	}, workRoot)
	if err != nil {
		run.Status = "failed"
		run.Error = "release: build: " + err.Error()
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	run.ManifestSHA256 = report.ManifestSHA256
	run.OutputDir = report.OutputDir
	run.Artifacts = report.Artifacts
	run.BuildOutput = report.BuildOutput
	if !report.OK {
		run.Status = "failed"
		run.Error = report.Error
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	run.Notes = BuildNote(run.Version, candidates, report.Artifacts, req.Notes, time.Now())
	if req.DryRun {
		run.Status = "built"
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}

	// tag 必须指向本次构建的提交；已存在且指向别处时拒绝，避免覆盖历史版本。
	existing, err := forge.TagSHA(ctx, req.Repo, run.Version)
	if err != nil {
		run.Status = "failed"
		run.Error = "release: read tag: " + err.Error()
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	if existing != "" && !strings.HasPrefix(commit, existing) && !strings.HasPrefix(existing, commit) {
		run.Status = "failed"
		run.Error = fmt.Sprintf("release: tag %s already points to %s", run.Version, existing)
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	if existing == "" {
		if err := forge.CreateTagRef(ctx, req.Repo, run.Version, commit); err != nil {
			run.Status = "failed"
			run.Error = "release: create tag: " + err.Error()
			run.FinishedAt = time.Now().UTC()
			run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
			return run, nil
		}
	}
	run.Tag = run.Version
	ghRelease, err := forge.CreateRelease(ctx, req.Repo, run.Version, commit, run.Version, run.Notes)
	if err != nil {
		run.Status = "failed"
		run.Error = "release: create GitHub release: " + err.Error()
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
		return run, nil
	}
	run.ReleaseID = ghRelease.ID
	run.ReleaseURL = ghRelease.HTMLURL

	// 制品挂到 Release 附件上，用户从 Release 页直接下载；失败只记 warning，不回滚已发布的 tag。
	for i := range run.Artifacts {
		art := &run.Artifacts[i]
		local := art.LocalPath
		if local == "" {
			local = art.Path
		}
		data, rerr := os.ReadFile(local)
		if rerr != nil {
			run.Artifacts[i].UploadError = "read: " + rerr.Error()
			continue
		}
		asset, uerr := forge.UploadReleaseAsset(ctx, req.Repo, ghRelease.ID, filepath.Base(art.Path), data)
		if uerr != nil {
			run.Artifacts[i].UploadError = uerr.Error()
			continue
		}
		run.Artifacts[i].DownloadURL = asset.BrowserDownloadURL
	}

	var warnings []string
	for _, c := range candidates {
		if err := forge.RemoveLabel(ctx, req.Repo, c.Number, req.ReadyLabel); err != nil {
			warnings = append(warnings, fmt.Sprintf("#%d remove label: %v", c.Number, err))
			continue
		}
		if err := forge.AddLabels(ctx, req.Repo, c.Number, []string{req.ReleasedLabel}); err != nil {
			warnings = append(warnings, fmt.Sprintf("#%d add label: %v", c.Number, err))
			continue
		}
		if err := forge.Comment(ctx, req.Repo, c.Number, fmt.Sprintf("已随版本 `%s` 发布：%s", run.Version, run.ReleaseURL)); err != nil {
			warnings = append(warnings, fmt.Sprintf("#%d comment: %v", c.Number, err))
		}
	}
	run.Status = "released"
	if len(warnings) > 0 {
		run.Status = "released_with_warnings"
		run.Error = strings.Join(warnings, "; ")
	}
	if notify != nil {
		if err := notify.Notify(ctx, req.ProjectID, BuildNotifyText(run.Version, run.ReleaseURL, candidates, run.Artifacts)); err != nil {
			run.NotifyStatus = "failed"
			run.NotifyError = err.Error()
		} else {
			run.NotifyStatus = "sent"
		}
	}
	run.FinishedAt = time.Now().UTC()
	run.DurationMS = run.FinishedAt.Sub(started).Milliseconds()
	return run, nil
}

// Rollback 撤销一次已发布版本：删 Release 与 tag，把候选 Issue 退回 ready 并通知。
func Rollback(ctx context.Context, run *Run, repo string, forge Forge, notify Notifier, readyLabel, releasedLabel string) error {
	if run == nil {
		return fmt.Errorf("release: run is required")
	}
	if run.Status != "released" && run.Status != "released_with_warnings" {
		return fmt.Errorf("release: run %d in status %q cannot be rolled back", run.ID, run.Status)
	}
	if readyLabel == "" {
		readyLabel = "smartpipe:ready"
	}
	if releasedLabel == "" {
		releasedLabel = "smartpipe:released"
	}
	var problems []string
	if run.ReleaseID > 0 {
		if err := forge.DeleteRelease(ctx, repo, run.ReleaseID); err != nil {
			problems = append(problems, "delete release: "+err.Error())
		}
	}
	if run.Tag != "" {
		if err := forge.DeleteTagRef(ctx, repo, run.Tag); err != nil {
			problems = append(problems, "delete tag: "+err.Error())
		}
	}
	for _, c := range run.Candidates {
		if err := forge.AddLabels(ctx, repo, c.Number, []string{readyLabel}); err != nil {
			problems = append(problems, fmt.Sprintf("#%d re-add ready: %v", c.Number, err))
		}
		if err := forge.RemoveLabel(ctx, repo, c.Number, releasedLabel); err != nil {
			problems = append(problems, fmt.Sprintf("#%d remove released: %v", c.Number, err))
		}
		if err := forge.Comment(ctx, repo, c.Number, fmt.Sprintf("版本 `%s` 已回滚，Issue 恢复待发版状态。", run.Version)); err != nil {
			problems = append(problems, fmt.Sprintf("#%d comment: %v", c.Number, err))
		}
	}
	if notify != nil && run.ProjectID != "" {
		text := fmt.Sprintf("【SmartPipe 回滚】%s 已回滚，%d 个 Issue 恢复待发版。", run.Version, len(run.Candidates))
		if len(problems) > 0 {
			text += "\n注意：" + strings.Join(problems, "; ")
		}
		if err := notify.Notify(ctx, run.ProjectID, text); err != nil {
			problems = append(problems, "notify: "+err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("release: rollback incomplete: %s", strings.Join(problems, "; "))
	}
	return nil
}
