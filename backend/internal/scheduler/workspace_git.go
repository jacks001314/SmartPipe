package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GitWorkspace 为每个 Issue 创建独立 worktree，共享项目的 canonical clone。
type GitWorkspace struct {
	Root     string // worktree 根目录
	RepoRoot string // 每个项目一份 mirror 的存放目录
	GitBin   string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewGitWorkspace 构造 worktree 管理器。
func NewGitWorkspace(root, repoRoot, gitBin string) *GitWorkspace {
	if gitBin == "" {
		gitBin = "git"
	}
	return &GitWorkspace{Root: root, RepoRoot: repoRoot, GitBin: gitBin, locks: map[string]*sync.Mutex{}}
}

// mirror 返回项目的 bare 镜像路径。
func (g *GitWorkspace) mirror(p *Project) string {
	if g.RepoRoot == "" {
		return p.LocalPath
	}
	return filepath.Join(g.RepoRoot, p.ID+".git")
}

func (g *GitWorkspace) repoLock(projectID string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locks == nil {
		g.locks = map[string]*sync.Mutex{}
	}
	l := g.locks[projectID]
	if l == nil {
		l = &sync.Mutex{}
		g.locks[projectID] = l
	}
	return l
}

// Dir 返回 Issue 的 worktree 路径。
func (g *GitWorkspace) Dir(p *Project, issue int) string {
	return filepath.Join(g.Root, p.ID, fmt.Sprintf("issue-%d", issue))
}

// Branch 返回 Issue 的分支名。
func (g *GitWorkspace) Branch(issue int) string { return fmt.Sprintf("agent/issue-%d", issue) }

func (g *GitWorkspace) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.GitBin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Prepare 幂等地创建 worktree。preserve=true 表示继续已有的 PR 分支，
// 保留 worktree 里的历史提交；否则回到基线，避免把失败尝试的提交叠加进来。
func (g *GitWorkspace) Prepare(ctx context.Context, p *Project, issue int, preserve bool) (string, string, error) {
	if strings.TrimSpace(p.LocalPath) == "" {
		return "", "", fmt.Errorf("scheduler: project %s has no local repo", p.ID)
	}
	if strings.TrimSpace(g.Root) == "" {
		return "", "", fmt.Errorf("scheduler: workspace root is empty")
	}
	branch := g.Branch(issue)
	dir := g.Dir(p, issue)
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil && st != nil {
		if preserve {
			// 已有 PR 的追问：保留分支历史，新提交才能快进推上去。
			return dir, branch, nil
		}
		if err := g.resetExisting(ctx, p, dir, branch); err != nil {
			return "", "", err
		}
		return dir, branch, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}

	lock := g.repoLock(p.ID)
	lock.Lock()
	defer lock.Unlock()

	repo := g.mirror(p)
	if _, err := os.Stat(repo); err != nil {
		if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
			return "", "", err
		}
		if out, err := g.run(ctx, "", "clone", "--mirror", p.LocalPath, repo); err != nil {
			return "", "", fmt.Errorf("scheduler: mirror %s: %w: %s", p.ID, err, out)
		}
	} else if out, err := g.run(ctx, repo, "fetch", "--no-prune", "--all"); err != nil {
		// 拉取失败不影响后续（基分支可能已经存在），但绝不能 prune：
		// mirror 里的 agent/* 分支只存在于本机，且可能正被别的 worktree 使用。
		// 一旦 prune 掉，那个 worktree 的 HEAD 会变成未出生分支，下一次提交
		// 就成了孤儿根提交，PR 会因“没有共同祖先”被 GitHub 拒绝。
		_ = out
	}

	base := firstNonEmpty(p.DefaultBase, "main")
	ref := ""
	for _, candidate := range []string{"refs/heads/" + base, "origin/" + base, base} {
		if _, err := g.run(ctx, repo, "rev-parse", "--verify", candidate); err == nil {
			ref = candidate
			break
		}
	}
	if ref == "" {
		return "", "", fmt.Errorf("scheduler: base branch %q not found in %s", base, p.ID)
	}
	if out, err := g.run(ctx, repo, "worktree", "add", "-B", branch, dir, ref); err != nil {
		// 目录可能残留：清掉后再试一次。
		_, _ = g.run(ctx, p.LocalPath, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
		if out2, err2 := g.run(ctx, repo, "worktree", "add", "-B", branch, dir, ref); err2 != nil {
			return "", "", fmt.Errorf("scheduler: add worktree: %w: %s %s", err, out, out2)
		}
	}
	return dir, branch, nil
}

// Cleanup 删除 worktree（分支保留 7 天由外部策略回收）。
func (g *GitWorkspace) Cleanup(ctx context.Context, p *Project, issue int) error {
	if strings.TrimSpace(p.LocalPath) == "" {
		return nil
	}
	dir := g.Dir(p, issue)
	lock := g.repoLock(p.ID)
	lock.Lock()
	defer lock.Unlock()
	_, err := g.run(ctx, g.mirror(p), "worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
	return err
}

// resetExisting 把已存在的工作区强制拉回项目基线的提交。
func (g *GitWorkspace) resetExisting(ctx context.Context, p *Project, dir, branch string) error {
	repo := g.mirror(p)
	base := firstNonEmpty(p.DefaultBase, "main")
	ref := ""
	for _, candidate := range []string{"refs/heads/" + base, "origin/" + base, base} {
		if _, err := g.run(ctx, repo, "rev-parse", "--verify", candidate); err == nil {
			ref = candidate
			break
		}
	}
	if ref == "" {
		return nil
	}
	sha, err := g.run(ctx, repo, "rev-parse", ref)
	if err != nil {
		return fmt.Errorf("scheduler: resolve base %s: %w", ref, err)
	}
	if _, err := g.run(ctx, dir, "checkout", "-B", branch, sha); err != nil {
		return fmt.Errorf("scheduler: checkout branch: %w", err)
	}
	if _, err := g.run(ctx, dir, "reset", "--hard", sha); err != nil {
		return fmt.Errorf("scheduler: reset workspace: %w", err)
	}
	if _, err := g.run(ctx, dir, "clean", "-fd"); err != nil {
		return fmt.Errorf("scheduler: clean workspace: %w", err)
	}
	return nil
}
