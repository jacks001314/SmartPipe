package patch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Verifier 在干净副本上校验补丁并执行 smartpipe.yaml 中的命令。
type Verifier struct {
	GitBin       string
	WorkRoot     string
	Limits       Limits
	DenyPrefixes []string
	ProfileName  string
	// AllowMissingProfile 仅用于本地调试；合并门禁必须为 false。
	AllowMissingProfile bool
}

// CheckResult 是单条验证命令的结果。
type CheckResult struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
	TimedOut   bool   `json:"timed_out"`
}

// Report 是一次补丁验证的完整证据。
type Report struct {
	OK           bool              `json:"ok"`
	Error        string            `json:"error,omitempty"`
	IssueID      string            `json:"issue_id,omitempty"`
	PatchHash    string            `json:"patch_hash"`
	BaseCommit   string            `json:"base_commit"`
	HeadCommit   string            `json:"head_commit,omitempty"`
	Files        []string          `json:"files,omitempty"`
	ChangedLines int               `json:"changed_lines"`
	Checks       []CheckResult     `json:"checks,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
	// EnvironmentError 表示验证环境本身不可用（仓库不存在、checkout 失败等），
	// 与补丁质量无关，调用方不应因此重跑模型。
	EnvironmentError bool      `json:"environment_error,omitempty"`
	DurationMS       int64     `json:"duration_ms"`
	WorkDir          string    `json:"work_dir,omitempty"`
	VerifiedAt       time.Time `json:"verified_at"`
}

func (v *Verifier) gitBin() string {
	if strings.TrimSpace(v.GitBin) != "" {
		return v.GitBin
	}
	return "git"
}

func (v *Verifier) profileName() string {
	if strings.TrimSpace(v.ProfileName) != "" {
		return v.ProfileName
	}
	return "smartpipe.yaml"
}

// Verify 执行完整校验；结构性失败通过 Report.OK=false 返回，err 只表示无法开始。
func (v *Verifier) Verify(ctx context.Context, b Bundle, repoSource string) (*Report, error) {
	started := time.Now()
	rep := &Report{
		IssueID:     b.IssueID,
		PatchHash:   PatchHash(b.Patch),
		BaseCommit:  strings.TrimSpace(b.BaseCommit),
		Environment: b.Environment,
		VerifiedAt:  started.UTC(),
		DurationMS:  0,
	}
	files, err := Validate(b, v.Limits, v.DenyPrefixes)
	if err != nil {
		rep.Error = err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	rep.Files = files
	rep.ChangedLines = ChangedLineCount(b.Patch)

	if strings.TrimSpace(v.WorkRoot) != "" {
		if err := os.MkdirAll(v.WorkRoot, 0o755); err != nil {
			return rep, fmt.Errorf("patch: create work root: %w", err)
		}
	}
	root, err := os.MkdirTemp(v.WorkRoot, "smartpipe-verify-*")
	if err != nil {
		return rep, fmt.Errorf("patch: create workdir: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	repoDir := filepath.Join(root, "repo")
	patchFile := filepath.Join(root, "change.diff")
	if err := os.WriteFile(patchFile, []byte(NormalizePatch(b.Patch)), 0o600); err != nil {
		return rep, err
	}

	if _, err := v.runGit(ctx, "", "clone", "--no-hardlinks", "--quiet", repoSource, repoDir); err != nil {
		rep.Error = "clone failed: " + err.Error()
		rep.EnvironmentError = true
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	if _, err := v.runGit(ctx, repoDir, "checkout", "--detach", rep.BaseCommit); err != nil {
		rep.Error = "checkout base commit failed: " + err.Error()
		rep.EnvironmentError = true
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	head, err := v.runGit(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		rep.Error = "rev-parse failed: " + err.Error()
		rep.EnvironmentError = true
		return rep, nil
	}
	rep.HeadCommit = strings.TrimSpace(head)
	if !strings.HasPrefix(rep.HeadCommit, rep.BaseCommit) {
		rep.Error = fmt.Sprintf("base commit mismatch: requested %s, checked out %s", rep.BaseCommit, rep.HeadCommit)
		rep.EnvironmentError = true
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}

	if _, err := v.runGit(ctx, repoDir, "apply", "--check", "--whitespace=nowarn", patchFile); err != nil {
		rep.Error = "patch does not apply cleanly: " + err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	if _, err := v.runGit(ctx, repoDir, "apply", "--whitespace=nowarn", patchFile); err != nil {
		rep.Error = "patch apply failed: " + err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}

	profilePath := filepath.Join(repoDir, v.profileName())
	profile, err := LoadProfile(profilePath)
	if err != nil {
		if !v.AllowMissingProfile {
			rep.Error = err.Error()
			rep.DurationMS = time.Since(started).Milliseconds()
			return rep, nil
		}
		rep.Error = "profile missing, verification skipped (debug mode): " + err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}

	ok := true
	for _, name := range profile.MergeChecks() {
		res := runShellCheck(ctx, repoDir, name, profile.Commands[name], profile.TimeoutFor(name))
		rep.Checks = append(rep.Checks, res)
		if !res.OK {
			ok = false
			break
		}
	}
	rep.OK = ok && rep.Error == ""
	rep.DurationMS = time.Since(started).Milliseconds()
	if !ok {
		rep.Error = "verification command failed"
	}
	cleanup = true
	return rep, nil
}

func (v *Verifier) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// 强制关闭 autocrlf，保证补丁上下文与干净检出内容一致（Windows 默认会转换换行）。
	full := append([]string{"-c", "core.autocrlf=false"}, args...)
	cmd := exec.CommandContext(ctx, v.gitBin(), full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

func runShellCheck(ctx context.Context, dir, name, command string, timeout time.Duration) CheckResult {
	started := time.Now()
	res := CheckResult{Name: name, Command: command}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=1", "SMARTPIPE_VERIFY=1", "GIT_TERMINAL_PROMPT=0")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	res.DurationMS = time.Since(started).Milliseconds()
	res.Output = truncate(buf.String(), 20000)
	if runCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		res.OK = false
		return res
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Output = strings.TrimSpace(res.Output + "\n" + err.Error())
		}
		res.OK = false
		return res
	}
	res.OK = true
	return res
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}
