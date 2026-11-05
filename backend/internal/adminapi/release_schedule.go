package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/release"
	"smartpipe/backend/internal/store"
)

// StartReleaseScheduler 每天在指定时刻（HH:MM，本地时区）对启用的项目跑一次发版。
// 没有候选 Issue 的项目会记为 skipped；同一项目同一天同一版本只跑一次。
func (a *API) StartReleaseScheduler(ctx context.Context, at string) {
	hour, minute, ok := parseClock(at)
	if !ok {
		if strings.TrimSpace(at) != "" {
			log.Printf("发版定时器: 无法解析 %q（期望 HH:MM），已停用", at)
		}
		return
	}
	log.Printf("发版定时器: 每天 %02d:%02d 执行", hour, minute)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		lastDay := ""
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if now.Hour() != hour || now.Minute() != minute {
					continue
				}
				day := now.Format("2006-01-02")
				if lastDay == day {
					continue
				}
				lastDay = day
				a.runScheduledReleases(ctx, now)
			}
		}
	}()
}

func parseClock(at string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(at), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func (a *API) runScheduledReleases(ctx context.Context, now time.Time) {
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		log.Printf("发版定时器: 读取项目失败: %v", err)
		return
	}
	version := "v" + now.Format("2006.01.02")
	for _, project := range projects {
		if !project.Enabled || strings.TrimSpace(project.GitHubRepoURL) == "" {
			continue
		}
		exists, err := a.store.HasReleaseRun(ctx, project.ID, version)
		if err != nil {
			log.Printf("发版定时器: %s 查询历史失败: %v", project.ID, err)
			continue
		}
		if exists {
			continue
		}
		record, run, err := a.executeReleaseRun(ctx, &project, releaseRunRequest{
			ProjectID: project.ID, Version: version, Trigger: "scheduled",
		})
		if err != nil {
			log.Printf("发版定时器: %s 执行失败: %v", project.ID, err)
			continue
		}
		pid := project.ID
		detail := map[string]any{"version": run.Version, "status": run.Status, "trigger": "scheduled",
			"candidates": len(run.Candidates), "release_url": run.ReleaseURL}
		_ = a.store.AppendAudit(ctx, store.AuditEntry{
			Actor: "release-scheduler", Action: "release.run", TargetType: "release_run",
			TargetID: strconv.FormatInt(record.ID, 10), ProjectID: &pid, Result: run.Status,
			Detail: detail, TraceID: fmt.Sprintf("sched-%d", now.Unix()),
		})
	}
}

// executeReleaseRun 是手工与定时发版共用的执行+落库逻辑。
func (a *API) executeReleaseRun(ctx context.Context, project *store.Project, in releaseRunRequest) (*store.ReleaseRun, *release.Run, error) {
	repo := strings.TrimSpace(project.GitHubRepoURL)
	if repo == "" {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	if repo == "" {
		return nil, nil, fmt.Errorf("adminapi: project %s has no GitHub repository", project.ID)
	}
	// 大版本人工确认：非 dry-run 且主版本号高于上次发布时，必须显式确认。
	if !in.DryRun {
		if last, lerr := a.store.LatestReleasedVersion(ctx, project.ID); lerr == nil && last != "" {
			if needsMajorConfirm(last, in.Version) && !in.ConfirmMajor {
				return nil, nil, &majorConfirmError{Last: last, Next: in.Version}
			}
		}
	}
	client, err := bridge.ClientFromCredentials(ctx, a.store, &project.ID)
	if err != nil {
		return nil, nil, err
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = strings.TrimSpace(project.GitHubDefaultBranch)
	}
	ready, released := a.releaseLabels(project, in.ReadyLabel)
	// 私有仓库的构建需要凭据：复用推送用的鉴权地址；公开仓库或本地裸库会原样返回。
	cloneURL := repo
	if u, cerr := a.resolvePushRemote(ctx, project, repo); cerr == nil { cloneURL = u }

	run, _ := release.Execute(ctx, release.RunRequest{
		ProjectID: project.ID, Repo: repo, Version: strings.TrimSpace(in.Version),
		Commit: strings.TrimSpace(in.Commit), Branch: branch,
		ReadyLabel: ready, ReleasedLabel: released,
		CloneURL: cloneURL, ArtifactGlobs: in.ArtifactGlobs, Notes: in.Notes, DryRun: in.DryRun, Trigger: in.Trigger,
	}, client, a.releaseNotifier(), release.Build, "")
	if run == nil {
		return nil, nil, fmt.Errorf("adminapi: release run produced no result")
	}
	candidates, _ := json.Marshal(run.Candidates)
	artifacts, _ := json.Marshal(run.Artifacts)
	pid := project.ID
	record, err := a.store.CreateReleaseRun(ctx, store.ReleaseRun{
		ProjectID: &pid, Version: run.Version, CommitSHA: run.Commit, Branch: run.Branch, Status: run.Status,
		Trigger: run.Trigger, Candidates: candidates, Notes: run.Notes, ReleaseURL: run.ReleaseURL,
		Tag: run.Tag, ReleaseID: run.ReleaseID, ManifestSHA256: run.ManifestSHA256, OutputDir: run.OutputDir,
		Artifacts: artifacts, BuildOutput: truncateText(run.BuildOutput, 20000), Error: run.Error,
		NotifyStatus: run.NotifyStatus, NotifyError: run.NotifyError,
		StartedAt: run.StartedAt, FinishedAt: &run.FinishedAt, DurationMS: int(run.DurationMS),
	})
	if err != nil {
		return nil, run, err
	}
	run.ID = record.ID
	return record, run, nil
}
