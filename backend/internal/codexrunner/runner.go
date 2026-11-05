// Package codexrunner 用 codex-sdk-go 在一个已检出的工作区里执行一次开发任务，
// 并把模型改动整理成可交给 patch 门禁验证的 unified diff。
//
// 这里刻意不自己拼 `codex exec` 命令：SDK 走 app-server 的 JSON-RPC，能拿到
// turn 状态、最终回复和 token 用量，也避免命令行参数随版本漂移。
// 模型永远拿不到仓库凭据——它只在本地工作区里改文件，提交与推送仍由
// patch 门禁和服务端的 Git 凭据完成。
package codexrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	codex "github.com/pmenglund/codex-sdk-go"
)

// 默认值：一次开发任务最多 30 分钟，effort 交给模型默认。
const defaultTimeout = 30 * time.Minute

// ErrInvalid 表示调用参数不完整或非法。
var ErrInvalid = errors.New("codexrunner: invalid spec")

// Spec 是一次开发任务的输入。
type Spec struct {
	// WorkDir 是 codex 的工作目录，必须是已检出的仓库根目录。
	WorkDir string
	// Prompt 是交给模型的完整任务描述。
	Prompt string
	// CodexPath 是 codex 可执行文件路径，默认取 PATH 中的 codex。
	CodexPath string
	// Model 为空时使用 Codex 当前默认模型。
	Model string
	// Effort 取 low/medium/high 等标准值，空表示不指定。
	Effort string
	// BaseInstructions 可覆盖模型的基础指令。
	BaseInstructions string
	// Timeout 是整轮执行的超时，默认 30 分钟。
	Timeout time.Duration
	// Env 是追加给 codex 进程的环境变量，形如 KEY=value。
	Env []string
	// LogWriter 接收 SDK 与服务端 stderr 诊断，默认丢弃。
	LogWriter io.Writer
	// ModelOutput 为 true 时把最终回复写进结果，否则只保留长度。
	KeepResponse bool
}

// Result 是一次执行的可核对结果。
type Result struct {
	Status        string `json:"status"`
	TurnID        string `json:"turn_id,omitempty"`
	ThreadID      string `json:"thread_id,omitempty"`
	FinalResponse string `json:"final_response,omitempty"`
	ResponseBytes int    `json:"response_bytes"`
	ItemCount     int    `json:"item_count"`
	DurationMS    int64  `json:"duration_ms"`
	TimedOut      bool   `json:"timed_out"`
}

func (s Spec) validate() error {
	if strings.TrimSpace(s.WorkDir) == "" {
		return fmt.Errorf("%w: work_dir is required", ErrInvalid)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("%w: prompt is required", ErrInvalid)
	}
	return nil
}

// Run 启动一次 codex 会话并跑到 turn 结束。err 表示无法开始或中途失败；
// 模型正常结束但状态异常时通过 Result.Status 返回。
func Run(ctx context.Context, spec Spec) (*Result, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logs := spec.LogWriter
	if logs == nil {
		logs = io.Discard
	}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	started := time.Now()
	client, err := codex.New(runCtx, codex.Options{
		Spawn: codex.SpawnOptions{
			CodexPath: strings.TrimSpace(spec.CodexPath),
			Env:       spec.Env,
			Stderr:    logs,
		},
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("codexrunner: start app-server: %w", err)
	}
	defer func() { _ = client.Close() }()

	ephemeral := true
	thread, err := client.StartThread(runCtx, codex.ThreadStartOptions{
		Cwd:              spec.WorkDir,
		Model:            strings.TrimSpace(spec.Model),
		ApprovalPolicy:   codex.ApprovalPolicyNever,
		SandboxPolicy:    codex.SandboxModeWorkspaceWrite,
		BaseInstructions: strings.TrimSpace(spec.BaseInstructions),
		Ephemeral:        &ephemeral,
	})
	if err != nil {
		return nil, fmt.Errorf("codexrunner: start thread: %w", err)
	}

	turnOpts := &codex.TurnOptions{}
	if effort := strings.TrimSpace(spec.Effort); effort != "" {
		turnOpts.Effort = codex.ReasoningEffort(effort)
	}
	turn, err := thread.Run(runCtx, spec.Prompt, turnOpts)
	if err != nil {
		out := &Result{Status: "error", DurationMS: time.Since(started).Milliseconds(), TimedOut: runCtx.Err() == context.DeadlineExceeded}
		return out, fmt.Errorf("codexrunner: run turn: %w", err)
	}
	result := &Result{
		Status:     turn.Status,
		TurnID:     turn.TurnID,
		ThreadID:   thread.ID(),
		ItemCount:  len(turn.Items),
		DurationMS: time.Since(started).Milliseconds(),
	}
	if turn.ErrorMessage != "" {
		result.Status = "error"
		result.FinalResponse = strings.TrimSpace(turn.ErrorMessage)
		result.ResponseBytes = len(result.FinalResponse)
		return result, fmt.Errorf("codexrunner: turn failed: %s", turn.ErrorMessage)
	}
	if spec.KeepResponse {
		result.FinalResponse = turn.FinalResponse
	} else {
		result.FinalResponse = ""
	}
	result.ResponseBytes = len(turn.FinalResponse)
	return result, nil
}

// Diff 把工作区当前改动整理成 unified diff：先 git add -A（按 .gitignore 过滤），
// 再输出暂存区相对 HEAD 的补丁。返回空补丁表示模型没有改任何文件。
//
// exclude 里的路径（相对 workDir 或绝对路径）不会进入补丁，用于排除任务描述、
// 补丁输出这类执行器自己写进去的辅助文件。
func Diff(ctx context.Context, workDir, gitBin string, exclude ...string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("%w: work_dir is required", ErrInvalid)
	}
	if strings.TrimSpace(gitBin) == "" {
		gitBin = "git"
	}
	addArgs := []string{"add", "-A", "--", "."}
	for _, raw := range exclude {
		if rel, ok := relativeTo(workDir, raw); ok {
			addArgs = append(addArgs, ":(exclude)"+rel)
		}
	}
	if _, err := git(ctx, gitBin, workDir, addArgs...); err != nil {
		return "", err
	}
	out, err := git(ctx, gitBin, workDir, "diff", "--cached", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/")
	if err != nil {
		return "", err
	}
	return out, nil
}

func git(ctx context.Context, bin, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("codexrunner: git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// relativeTo 把 path 换算成相对 workDir 的 slash 路径；不在工作区内时返回 false。
func relativeTo(workDir, path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	base, err := filepath.Abs(workDir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
