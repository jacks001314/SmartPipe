package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
)

// registerRepoSync 挂载「开跑前同步本地仓库」接口。
//
// 调度器不持有 GitHub 凭据，只能从 admin-api 维护的本地 clone 更新自己的
// mirror。本地 clone 的 origin 是 https 地址，没人带凭据去拉，所以基分支会
// 一直停在克隆那天。调度器每次准备 worktree 之前调用这里，让 admin-api 用
// 凭据把 clone 拉到远端最新，worktree 就总是基于当前 main。
func (a *API) registerRepoSync(party iris.Party) {
	party.Post("/repos/sync", a.syncProjectRepo)
}

type repoSyncResult struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Head    string `json:"head"`
	Updated bool   `json:"updated"`
	Note    string `json:"note,omitempty"`
}

func (a *API) syncProjectRepo(c iris.Context) {
	var in struct {
		ProjectID string `json:"project_id"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if in.ProjectID == "" {
		a.badRequest(c, "project_id 必填")
		return
	}
	ctx := c.Request().Context()
	project, err := a.store.GetProject(ctx, in.ProjectID)
	if err != nil {
		a.fail(c, err)
		return
	}
	dir := strings.TrimSpace(project.LocalRepoPath)
	if dir == "" {
		c.StatusCode(http.StatusConflict)
		_ = c.JSON(envelope{Err: &apiError{Code: "repo_not_cloned", Message: "项目还没有本地 clone，先在仓库页完成克隆"}})
		return
	}
	remote := strings.TrimSpace(project.GitHubRepoURL)
	if remote == "" {
		c.StatusCode(http.StatusConflict)
		_ = c.JSON(envelope{Err: &apiError{Code: "repo_not_configured", Message: "项目未配置 GitHub 仓库"}})
		return
	}
	token, terr := gitToken(ctx, a.store, &project.ID)
	if terr != nil {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "git_credential_unavailable", Message: terr.Error()}})
		return
	}
	branch := strings.TrimSpace(project.GitHubDefaultBranch)
	if branch == "" {
		branch = "main"
	}
	res, err := syncLocalClone(ctx, dir, remote, token, branch)
	if err != nil {
		a.audit(ctx, c, "repo.sync", "project", project.ID, &project.ID, "error", map[string]any{"error": err.Error()})
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "repo_sync_failed", Message: err.Error()}})
		return
	}
	res.Path = dir
	a.audit(ctx, c, "repo.sync", "project", project.ID, &project.ID, "ok",
		map[string]any{"head": res.Head, "branch": res.Branch, "updated": res.Updated})
	_ = c.JSON(envelope{Data: res})
}

// syncLocalClone 用凭据把本地 clone 拉到远端最新：只更新远端跟踪分支，
// 再把当前分支快进到远端；不碰本地未提交改动之外的东西。
func syncLocalClone(ctx context.Context, dir, remote, token, branch string) (*repoSyncResult, error) {
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("本地仓库 %s 不存在或不是 git 目录", dir)
	}
	before, err := gitLine(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	res := &repoSyncResult{Branch: branch}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if _, err := runGitWithToken(ctx, dir, token,
		"fetch", "--no-prune", remote,
		"+refs/heads/*:refs/remotes/origin/*",
		"+refs/tags/*:refs/tags/*"); err != nil {
		return nil, fmt.Errorf("fetch %s: %w", remote, unwrapGitOutput(err))
	}
	target := "refs/remotes/origin/" + branch
	head, err := gitLine(ctx, dir, "rev-parse", "--verify", target)
	if err != nil {
		return nil, fmt.Errorf("远端没有分支 %s：%w", branch, unwrapGitOutput(err))
	}
	// 只有当前分支能快进时才移动它，避免把别人手改过的 clone 覆盖掉。
	res.Head = head
	if _, err := gitLine(ctx, dir, "merge-base", "--is-ancestor", before, head); err != nil {
		res.Note = "本地分支不是远端的祖先，保持原样未快进"
		return res, nil
	}
	if _, err := gitLine(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if _, err := gitLine(ctx, dir, "reset", "--hard", head); err != nil {
			return nil, fmt.Errorf("快进本地分支: %w", unwrapGitOutput(err))
		}
	}
	res.Updated = before != head
	return res, nil
}

func gitLine(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// runGitWithToken 用 askpass 把 token 交给 git，token 不出现在命令行里。
func runGitWithToken(ctx context.Context, dir, token string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		script, err := writeAskpass()
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(filepath.Dir(script))
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+script, "GIT_USERNAME=x-access-token", "SMARTPIPE_GIT_TOKEN="+token)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// unwrapGitOutput 让错误信息只保留 git 的原话，便于直接回给调用方。
func unwrapGitOutput(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		return fmt.Errorf("%s", msg[i+2:])
	}
	return err
}
