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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	codex "github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/codex-sdk-go/protocol"
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
	// Images 是随本轮一起发给模型的本地图片路径（截图、设计稿等）。
	Images []string
	// CodexPath 是 codex 可执行文件路径，默认取 PATH 中的 codex。
	CodexPath string
	// Model 为空时使用 Codex 当前默认模型。
	Model string
	// Effort 取 low/medium/high 等标准值，空表示不指定。
	Effort string
	// BaseInstructions 可覆盖模型的基础指令（不同角色的系统提示词）。
	BaseInstructions string
	// Sandbox 为空时使用 workspace-write；只读角色（如问答 worker）用 "read-only"。
	Sandbox string
	// Timeout 是整轮执行的超时，默认 30 分钟。
	Timeout time.Duration
	// Env 是追加给 codex 进程的环境变量，形如 KEY=value。
	Env []string
	// LogWriter 接收 SDK 与服务端 stderr 诊断，默认丢弃。
	LogWriter io.Writer
	// ModelOutput 为 true 时把最终回复写进结果，否则只保留长度。
	KeepResponse bool
	// OnEvent 可选地接收 app-server 的原始流式通知。调度器用它把 Codex 的
	// 消息、命令输出和文件变更实时投影到控制台；普通调用方可以留空。
	OnEvent func(Event)
}

// Event 是一条已按到达顺序读出的 Codex app-server 通知。
// Params 保留原始 JSON，避免 SDK 的 sanitized 类型丢掉 turnId 等字段。
type Event struct {
	Method string
	Params json.RawMessage
	At     time.Time
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

var envMu sync.Mutex

// applyEnvOverrides 把 KEY=VALUE 写入当前进程环境，返回恢复函数。
// 调用方串行执行，避免并发会话互相覆盖。
// sensitiveEnvKeys 是只给调度器自己用的凭据。Codex 子进程会继承父进程环境，
// 这些变量必须在 turn 期间摘掉，避免模型通过 shell 读到主密钥或服务令牌。
var sensitiveEnvKeys = []string{
	"SMARTPIPE_MASTER_KEY",
	"SMARTPIPE_DSN",
	"SMARTPIPE_ADMIN_TOKEN",
	"SMARTPIPE_BOT_INTERNAL_TOKEN",
	"INTERNAL_TOKEN",
	"SCHEDULER_TOKEN",
	"SMARTPIPE_VERIFY_TOKEN",
	"SMARTPIPE_RELEASE_TOKEN",
	"SMARTPIPE_CRASH_TOKEN",
	"GITHUB_WEBHOOK_SECRET",
	"SMARTPIPE_FRP_TOKEN",
}

func applyEnvOverrides(env []string) func() {
	envMu.Lock()
	type oldValue struct {
		value string
		set   bool
	}
	saved := map[string]oldValue{}
	remember := func(key string) {
		if _, seen := saved[key]; seen {
			return
		}
		v, exists := os.LookupEnv(key)
		saved[key] = oldValue{value: v, set: exists}
	}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		remember(key)
		_ = os.Setenv(key, value)
	}
	for _, key := range sensitiveEnvKeys {
		remember(key)
		_ = os.Unsetenv(key)
	}
	return func() {
		for key, old := range saved {
			if old.set {
				_ = os.Setenv(key, old.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
		envMu.Unlock()
	}
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
	// 开发机的 ~/.codex 会带模型目录、供应商和工具开关，某些网关（如 vLLM）
	// 会直接拒绝这些请求；worker 统一用独立配置目录。
	spec.Env = isolateCodexHome(spec.Env)
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
	// SDK 会把 spec.Env 追加在 os.Environ 之后；同名变量在子进程里可能取到旧值。
	// 这里先把覆盖项写进当前进程环境，保证配置台的供应商地址与密钥优先生效。
	restoreEnv := applyEnvOverrides(spec.Env)
	defer restoreEnv()
	client, err := codex.New(runCtx, codex.Options{
		Spawn: codex.SpawnOptions{
			CodexPath:       strings.TrimSpace(spec.CodexPath),
			Env:             spec.Env,
			Stderr:          logs,
			ConfigOverrides: modelConfigOverrides(spec.Env),
		},
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("codexrunner: start app-server: %w", err)
	}
	defer func() { _ = client.Close() }()

	ephemeral := true
	sandbox := codex.SandboxModeWorkspaceWrite
	switch strings.ToLower(strings.TrimSpace(spec.Sandbox)) {
	case "read-only":
		sandbox = codex.SandboxModeReadOnly
	case "danger-full-access":
		// 容器本身是隔离边界时使用：容器内无法创建 user namespace，
		// Codex 自带沙箱起不来，只能关掉内层沙箱。
		sandbox = codex.SandboxModeDangerFullAccess
	}
	thread, err := client.StartThread(runCtx, codex.ThreadStartOptions{
		Cwd:              spec.WorkDir,
		Model:            strings.TrimSpace(spec.Model),
		ApprovalPolicy:   codex.ApprovalPolicyNever,
		SandboxPolicy:    sandbox,
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

	// 用流式接口而不是 thread.Run：Run 会把每条通知都攒进内存，命令输出一大
	// （例如跑测试）就会把 SDK 的 64 条通知缓冲撑爆，报 notification overflow。
	// 这里持续消费、只保留最终消息，既不掉队也不吃内存。
	inputs := make([]codex.Input, 0, 1+len(spec.Images))
	inputs = append(inputs, codex.TextInput(spec.Prompt))
	for _, imagePath := range spec.Images {
		if strings.TrimSpace(imagePath) != "" {
			inputs = append(inputs, codex.LocalImageInput(strings.TrimSpace(imagePath)))
		}
	}
	stream, err := thread.RunStreamed(runCtx, inputs, turnOpts)
	if err != nil {
		return &Result{Status: "error", DurationMS: time.Since(started).Milliseconds()}, fmt.Errorf("codexrunner: start turn: %w", err)
	}
	defer stream.Close()

	result := &Result{ThreadID: thread.ID()}
	var finalText string
	var turnErrMsg string
	done := false
	for !done {
		note, err := stream.Next(runCtx)
		if err != nil {
			result.Status = "error"
			result.DurationMS = time.Since(started).Milliseconds()
			result.TimedOut = runCtx.Err() == context.DeadlineExceeded
			result.FinalResponse = finalText
			result.ResponseBytes = len(finalText)
			return result, fmt.Errorf("codexrunner: read turn events: %w", err)
		}
		if spec.OnEvent != nil {
			params := append(json.RawMessage(nil), note.Raw...)
			if len(params) == 0 && note.Params != nil {
				params, _ = json.Marshal(note.Params)
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						_, _ = fmt.Fprintf(logs, "codexrunner: event sink panic: %v\n", recovered)
					}
				}()
				spec.OnEvent(Event{Method: note.Method, Params: params, At: time.Now()})
			}()
		}
		switch value := note.Params.(type) {
		case protocol.ItemCompletedNotification:
			if raw := value.Item.RawJSON(); len(raw) > 0 {
				var item struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(raw, &item) == nil && item.Type == "agentMessage" && strings.TrimSpace(item.Text) != "" {
					finalText = item.Text
					result.ItemCount++
				}
			}
		case protocol.TurnNotification:
			if value.Turn.ID != "" {
				result.TurnID = value.Turn.ID
			}
			if value.Turn.Status != "" {
				result.Status = string(value.Turn.Status)
			}
		}
		switch note.Method {
		case "turn/failed":
			turnErrMsg = failureMessage(note.Raw)
			done = true
		case "turn/completed":
			done = true
		}
	}

	result.DurationMS = time.Since(started).Milliseconds()
	result.TimedOut = runCtx.Err() == context.DeadlineExceeded
	result.ResponseBytes = len(finalText)
	if spec.KeepResponse {
		result.FinalResponse = finalText
	}
	if turnErrMsg != "" {
		result.Status = "error"
		result.FinalResponse = strings.TrimSpace(turnErrMsg)
		return result, fmt.Errorf("codexrunner: turn failed: %s", turnErrMsg)
	}
	if result.Status == "" {
		result.Status = "completed"
	}
	return result, nil
}

// failureMessage 从 turn/failed 通知里取出可读错误。
func failureMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "turn failed"
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Turn struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		if msg := strings.TrimSpace(payload.Error.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(payload.Turn.Error.Message); msg != "" {
			return msg
		}
	}
	return truncate(string(raw), 600)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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

// modelConfigOverrides 把配置台注入的 OPENAI_* 变量翻译成 app-server 的
// --config 覆盖项。只设环境变量时 Codex 仍会使用 ~/.codex/config.toml 里的
// 默认供应商，项目选择的模型就形同虚设。
func modelConfigOverrides(env []string) []string {
	var base, wire string
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "OPENAI_BASE_URL":
			base = strings.TrimSpace(value)
		case "OPENAI_WIRE_API":
			wire = strings.TrimSpace(value)
		}
	}
	if base == "" {
		return nil
	}
	if wire == "" {
		wire = "responses"
	}
	return []string{
		"model_provider=smartpipe",
		"model_providers.smartpipe.name=SmartPipe",
		"model_providers.smartpipe.base_url=" + base,
		"model_providers.smartpipe.wire_api=" + wire,
		"model_providers.smartpipe.env_key=OPENAI_API_KEY",
	}
}

// isolateCodexHome 保证 worker 有独立的 CODEX_HOME。
// 优先用调用方显式传入的 CODEX_HOME，其次 SMARTPIPE_CODEX_HOME，
// 都没配置时退回到系统临时目录下的固定路径。
func isolateCodexHome(env []string) []string {
	for _, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(key) == "CODEX_HOME" {
			return env
		}
	}
	home := strings.TrimSpace(os.Getenv("SMARTPIPE_CODEX_HOME"))
	if home == "" {
		home = filepath.Join(os.TempDir(), "smartpipe-codex-home")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return env
	}
	return append(env, "CODEX_HOME="+home)
}
