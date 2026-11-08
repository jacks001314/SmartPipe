package adminapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
)

const githubCloneRootKey = "github_clone_root"

var repoFullNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)

type githubCloneRootView struct {
	Root string `json:"root"`
}

func (a *API) registerGitHubClone(party iris.Party) {
	// 进程重启后，之前中断的克隆不会继续，统一标记为失败。
	_ = a.store.ResetGitHubRepoClones(context.Background())
	party.Get("/github/clone-root", a.getGitHubCloneRoot)
	party.Put("/github/clone-root", a.putGitHubCloneRoot)
	party.Post("/github/repositories/clone", a.cloneGitHubRepository)
}

func (a *API) cloneRoot(ctx context.Context) string {
	if raw, err := a.store.GetSetting(ctx, githubCloneRootKey); err == nil && strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("SMARTPIPE_CLONE_ROOT")); raw != "" {
		return raw
	}
	return "/data/repos"
}

func (a *API) getGitHubCloneRoot(c iris.Context) {
	ctx := c.Request().Context()
	root := a.cloneRoot(ctx)
	_ = c.JSON(envelope{Data: iris.Map{"root": root, "exists": dirExists(root)}})
}

func (a *API) putGitHubCloneRoot(c iris.Context) {
	var in struct {
		Root string `json:"root"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	root := strings.TrimSpace(in.Root)
	if root == "" {
		a.badRequest(c, "克隆目录不能为空")
		return
	}
	if !filepath.IsAbs(root) {
		a.badRequest(c, "克隆目录必须是绝对路径")
		return
	}
	ctx := c.Request().Context()
	if err := os.MkdirAll(root, 0o755); err != nil {
		a.badRequest(c, "无法创建克隆目录："+err.Error())
		return
	}
	if err := a.store.SetSetting(ctx, githubCloneRootKey, filepath.Clean(root)); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "github.clone_root", "setting", githubCloneRootKey, nil, "ok", map[string]any{"root": root})
	_ = c.JSON(envelope{Data: iris.Map{"root": filepath.Clean(root), "exists": true}})
}

// cloneGitHubRepository 把仓库 clone 到指定目录（默认 克隆根目录/owner/repo），
// 成功后把本地路径记到仓库库，项目创建时就能直接选这个本地库。
func (a *API) cloneGitHubRepository(c iris.Context) {
	var in struct {
		FullName  string `json:"full_name"`
		TargetDir string `json:"target_dir"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	full := strings.TrimSpace(in.FullName)
	if !repoFullNamePattern.MatchString(full) {
		a.badRequest(c, "仓库名不合法，应形如 owner/repo")
		return
	}
	ctx := c.Request().Context()
	catalog, _, err := a.store.ListGitHubRepositories(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	var repo *githubRepoSummary
	for _, item := range catalog {
		if strings.EqualFold(item.FullName, full) {
			full = item.FullName
			repo = &githubRepoSummary{FullName: item.FullName, HTMLURL: item.HTMLURL, Branch: item.DefaultBranch, LocalPath: item.LocalPath}
			break
		}
	}
	if repo == nil {
		a.badRequest(c, "仓库不在仓库库中，请先在 GitHub 配置页刷新仓库列表")
		return
	}

	root := a.cloneRoot(ctx)
	target := strings.TrimSpace(in.TargetDir)
	switch {
	case target == "":
		target = filepath.Join(root, filepath.FromSlash(full))
	case !filepath.IsAbs(target):
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	existed := dirExists(target)
	if existed && isGitWorktree(target) {
		if gitHasHead(ctx, target) {
			if err := a.store.SetGitHubRepoCloneState(ctx, full, "cloned", target, ""); err != nil {
				a.fail(c, err)
				return
			}
			_ = c.JSON(envelope{Data: iris.Map{"full_name": full, "path": target, "status": "cloned", "existing": true}})
			return
		}
		// .git 存在但没有 HEAD，说明上次克隆中断了，清掉重来。
		_ = os.RemoveAll(target)
		existed = false
	}
	if existed {
		if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
			a.badRequest(c, "目标目录已存在且不是 git 仓库："+target)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		a.badRequest(c, "无法创建目标目录："+err.Error())
		return
	}

	cloneURL := strings.TrimSpace(repo.HTMLURL)
	if cloneURL == "" {
		cloneURL = "https://github.com/" + full
	}
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	auth := ""
	if token, terr := gitToken(ctx, a.store, nil); terr == nil {
		auth = token
	}
	proxy := a.githubProxyURL(ctx)
	branch := repo.Branch
	if err := a.store.SetGitHubRepoCloneState(ctx, full, "cloning", "", ""); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "github.repository.clone", "repository", full, nil, "ok", map[string]any{"path": target})
	// 克隆可能持续几分钟，放到后台执行；控制台轮询仓库库看状态。
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		log, cerr := runGitClone(bg, cloneURL, target, auth, proxy, branch, !existed)
		if cerr != nil {
			detail := strings.TrimSpace(log + "\n" + cerr.Error())
			_ = a.store.SetGitHubRepoCloneState(context.Background(), full, "failed", "", detail)
			return
		}
		_ = a.store.SetGitHubRepoCloneState(context.Background(), full, "cloned", target, "")
	}()
	_ = c.JSON(envelope{Data: iris.Map{
		"full_name": full, "path": target, "status": "cloning",
	}})
}

type githubRepoSummary struct {
	FullName  string
	HTMLURL   string
	Branch    string
	LocalPath string
}

// runGitClone 用 askpass 传 token，避免 token 出现在 git 命令行里。
func runGitClone(ctx context.Context, cloneURL, target, token, proxy, branch string, cleanupOnFail bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	args := []string{"clone", "--quiet"}
	if proxy != "" {
		args = append(args, "-c", "http.proxy="+proxy)
	}
	args = append(args, cloneURL, target)
	cmd := exec.CommandContext(ctx, "git", args...)
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
	text := string(out)
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("克隆超时（10 分钟）")
	}
	if err != nil {
		if cleanupOnFail {
			_ = os.RemoveAll(target)
		}
		return text, err
	}
	if branch != "" {
		checkout := exec.CommandContext(ctx, "git", "-C", target, "checkout", "--quiet", branch)
		checkout.Env = cmd.Env
		if extra, err := checkout.CombinedOutput(); err == nil {
			text += string(extra)
		}
	}
	return text, nil
}

func writeAskpass() (string, error) {
	dir, err := os.MkdirTemp("", "smartpipe-askpass-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n  *sername*) echo \"$GIT_USERNAME\" ;;\n  *) echo \"$SMARTPIPE_GIT_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

func gitHead(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func gitHasHead(ctx context.Context, dir string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "HEAD").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func isGitWorktree(path string) bool {
	return dirExists(filepath.Join(path, ".git"))
}

func truncateTail(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		if text == "" {
			return ""
		}
		return "：" + text
	}
	return "：" + text[len(text)-limit:]
}
