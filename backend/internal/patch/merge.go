package patch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// MergeRequest 描述一次补丁提交/合并；repo 必须是 lead 自己创建的干净克隆。
type MergeRequest struct {
	Repo         string `json:"repo"`
	BaseCommit   string `json:"base_commit"`
	Branch       string `json:"branch"`
	Patch        string `json:"patch"`
	Message      string `json:"message"`
	AuthorName   string `json:"author_name"`
	AuthorEmail  string `json:"author_email"`
	TargetBranch string `json:"target_branch"`
}

// MergeResult 是提交/合并后的证据。
type MergeResult struct {
	OK           bool   `json:"ok"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	TargetBranch string `json:"target_branch,omitempty"`
	TargetHead   string `json:"target_head,omitempty"`
	Message      string `json:"message,omitempty"`
	Error        string `json:"error,omitempty"`
}

// CommitAndMerge 在干净克隆上创建分支、应用补丁、提交，并按需合并到目标分支。
// 不执行 push：推送由 lead 用项目凭据单独完成。
func (v *Verifier) CommitAndMerge(ctx context.Context, req MergeRequest) (*MergeResult, error) {
	res := &MergeResult{Branch: strings.TrimSpace(req.Branch)}
	if strings.TrimSpace(req.Repo) == "" {
		return res, fmt.Errorf("patch: repo is required")
	}
	if _, err := Validate(Bundle{BaseCommit: req.BaseCommit, Patch: req.Patch}, v.Limits, v.DenyPrefixes); err != nil {
		res.Error = err.Error()
		return res, nil
	}
	repo := strings.TrimSpace(req.Repo)
	status, err := v.git(ctx, repo, "status", "--porcelain")
	if err != nil {
		res.Error = "not a git worktree: " + err.Error()
		return res, nil
	}
	if strings.TrimSpace(status) != "" {
		// Windows 上 clone 可能因 autocrlf 把工作区换成 CRLF；只有换行差异时按索引刷新，
		// 有真实改动或未跟踪文件仍拒绝。
		realDiff, _ := v.git(ctx, repo, "diff", "--ignore-cr-at-eol", "--name-only")
		untracked, _ := v.git(ctx, repo, "ls-files", "--others", "--exclude-standard")
		if strings.TrimSpace(realDiff) != "" || strings.TrimSpace(untracked) != "" {
			res.Error = "worktree is dirty; use a clean clone"
			return res, nil
		}
		if _, err := v.git(ctx, repo, "checkout", "--", "."); err != nil {
			res.Error = "cannot refresh EOL-only worktree: " + err.Error()
			return res, nil
		}
		if again, _ := v.git(ctx, repo, "status", "--porcelain"); strings.TrimSpace(again) != "" {
			res.Error = "worktree is dirty; use a clean clone"
			return res, nil
		}
	}
	base := strings.TrimSpace(req.BaseCommit)
	if _, err := v.git(ctx, repo, "cat-file", "-e", base+"^{commit}"); err != nil {
		res.Error = "base commit not found: " + err.Error()
		return res, nil
	}
	if res.Branch == "" {
		res.Branch = "agent/patch"
	}
	if _, err := v.git(ctx, repo, "checkout", "-B", res.Branch, base); err != nil {
		res.Error = "checkout branch failed: " + err.Error()
		return res, nil
	}
	patchFile, cleanup, err := writeTempPatch(req.Patch)
	if err != nil {
		return res, err
	}
	defer cleanup()
	if _, err := v.git(ctx, repo, "apply", "--check", "--whitespace=nowarn", patchFile); err != nil {
		res.Error = "patch does not apply cleanly: " + err.Error()
		return res, nil
	}
	if _, err := v.git(ctx, repo, "apply", "--whitespace=nowarn", patchFile); err != nil {
		res.Error = "patch apply failed: " + err.Error()
		return res, nil
	}
	if _, err := v.git(ctx, repo, "add", "-A"); err != nil {
		res.Error = "git add failed: " + err.Error()
		return res, nil
	}
	name := strings.TrimSpace(req.AuthorName)
	if name == "" {
		name = "smartpipe-lead"
	}
	email := strings.TrimSpace(req.AuthorEmail)
	if email == "" {
		email = "lead@smartpipe.local"
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		msg = "fix: apply verified patch"
	}
	commitArgs := []string{"-c", "user.name=" + name, "-c", "user.email=" + email,
		"commit", "-m", msg, "--author", name + " <" + email + ">"}
	if out, err := v.git(ctx, repo, commitArgs...); err != nil {
		res.Error = "git commit failed: " + err.Error() + " " + out
		return res, nil
	}
	commit, err := v.git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		res.Error = "rev-parse failed: " + err.Error()
		return res, nil
	}
	res.Commit = strings.TrimSpace(commit)

	target := strings.TrimSpace(req.TargetBranch)
	if target != "" {
		if _, err := v.git(ctx, repo, "checkout", target); err != nil {
			res.Error = "checkout target failed: " + err.Error()
			res.OK = true
			return res, nil
		}
		mergeMsg := "Merge branch '" + res.Branch + "'"
		if _, err := v.git(ctx, repo, "-c", "user.name="+name, "-c", "user.email="+email,
			"merge", "--no-ff", res.Branch, "-m", mergeMsg); err != nil {
			res.Error = "merge failed: " + err.Error()
			res.OK = true
			return res, nil
		}
		head, err := v.git(ctx, repo, "rev-parse", "HEAD")
		if err != nil {
			res.Error = "rev-parse target failed: " + err.Error()
			res.OK = true
			return res, nil
		}
		res.TargetBranch = target
		res.TargetHead = strings.TrimSpace(head)
	}
	res.OK = true
	res.Message = msg
	return res, nil
}

func (v *Verifier) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.autocrlf=false"}, args...)
	cmd := exec.CommandContext(ctx, v.gitBin(), full...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}
