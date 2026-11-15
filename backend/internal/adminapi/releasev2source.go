package adminapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

// releaseServiceHTTP 用于内部归档传输；源码包可能较大，超时放宽到 10 分钟。
var releaseServiceHTTP = &http.Client{Timeout: 10 * time.Minute}

// freezeReleaseSource 在运行创建后立即把源码固化到对象存储。
//
// 优先使用 GitHub 在该提交上的 tarball，保证内容与仓库一致；
// GitHub 不可达时退回 release-service 的本地镜像 git archive。
// 冻结失败会让本次运行直接取消，避免执行器拿到会变化的源码。
func (a *API) freezeReleaseSource(ctx context.Context, run *releasev2.Run, project *store.Project) error {
	if a.releaseServiceURL == "" {
		return fmt.Errorf("release-service 内部地址未配置")
	}
	if run == nil || project == nil {
		return fmt.Errorf("run 或 project 为空")
	}
	repo := firstNonEmpty(run.Repo, project.GitHubRepoURL, project.GitHubIssueRepo)
	commit := strings.TrimSpace(run.ResolvedCommit)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var githubErr error
	if repo != "" && commit != "" {
		client, err := bridge.ClientForRepo(ctx, a.store, &project.ID, repo)
		if err != nil {
			githubErr = err
		} else if client != nil {
			archive, err := client.DownloadArchive(ctx, repo, commit)
			if err != nil {
				githubErr = err
			} else {
				defer archive.Body.Close()
				if err := a.postRunSource(ctx, run.ID, archive.Body, ""); err != nil {
					githubErr = err
				} else {
					return nil
				}
			}
		}
	}
	if err := a.postRunSource(ctx, run.ID, nil, "mirror"); err != nil {
		if githubErr != nil {
			return fmt.Errorf("GitHub 归档失败(%v)；本地镜像归档失败(%w)", githubErr, err)
		}
		return err
	}
	return nil
}

// postRunSource 把归档流或本地镜像归档请求发给 release-service。
func (a *API) postRunSource(ctx context.Context, runID string, body io.Reader, from string) error {
	endpoint := strings.TrimRight(a.releaseServiceURL, "/") + "/internal/release-runs/" + url.PathEscape(runID) + "/source"
	if from != "" {
		endpoint += "?from=" + url.QueryEscape(from)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	if a.releaseServiceToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.releaseServiceToken)
	}
	resp, err := releaseServiceHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("release-service 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}
