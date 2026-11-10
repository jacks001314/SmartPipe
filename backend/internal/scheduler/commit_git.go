package scheduler

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitCommitter 在 Codex 的 worktree 里把改动提交成一次本地 commit。
// 推送由 admin-api 完成，这里不接触远端，也不碰凭据。
type GitCommitter struct {
	GitBin      string
	AuthorName  string
	AuthorEmail string
}

// Commit 暂存全部改动并提交，返回新提交的 SHA。
func (c GitCommitter) Commit(ctx context.Context, workDir, message string) (string, error) {
	bin := strings.TrimSpace(c.GitBin)
	if bin == "" {
		bin = "git"
	}
	name := strings.TrimSpace(c.AuthorName)
	if name == "" {
		name = "SmartPipe Codex"
	}
	email := strings.TrimSpace(c.AuthorEmail)
	if email == "" {
		email = "codex@smartpipe.local"
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	staged, err := run("diff", "--cached", "--name-only")
	if err != nil {
		return "", err
	}
	if staged == "" {
		return "", fmt.Errorf("scheduler: no staged changes")
	}
	if _, err := run("-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "-m", message, "--author", name+" <"+email+">"); err != nil {
		return "", err
	}
	return run("rev-parse", "HEAD")
}
