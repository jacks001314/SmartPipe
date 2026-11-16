// Package releaseagent 把一次发布运行交给独立的 Codex 会话执行。
//
// 设计边界：模型只通过 release MCP 的受控工具推进发布，构建机走 pull 模式，
// 会话凭证只以短期 token（环境变量引用）形式存在，不进入提示词或事件流。
package releaseagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/codexrunner"
	"smartpipe/backend/internal/releasev2"
)

var (
	// ErrDisabled 表示服务端没有开启发布 Agent。
	ErrDisabled = errors.New("releaseagent: disabled")
	// ErrAlreadyRunning 表示该运行已有活跃会话。
	ErrAlreadyRunning = errors.New("releaseagent: session already running")
	// ErrLimit 表示发布 Agent 的并发会话数已达上限。
	ErrLimit = errors.New("releaseagent: session limit reached")
)

// Repository 是发布 Agent 需要的存储能力。
type Repository interface {
	CreateReleaseV2Token(ctx context.Context, token releasev2.Token) error
	RevokeReleaseV2Token(ctx context.Context, id string) error
	CreateAgentSession(ctx context.Context, in *releasev2.AgentSession) error
	UpdateAgentSession(ctx context.Context, in *releasev2.AgentSession) error
	SetReleaseV2RunAgentThreadID(ctx context.Context, runID, threadID string) error
	MarkReleaseV2RunNeedsAttention(ctx context.Context, runID, reason string) (bool, error)
	AppendEvent(ctx context.Context, ev releasev2.Event) error
}

// Runner 执行一轮 Codex 会话；生产实现是 codexrunner.Run。
type Runner interface {
	Run(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error)
}

// RunnerFunc 让普通函数满足 Runner。
type RunnerFunc func(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error)

func (f RunnerFunc) Run(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error) {
	return f(ctx, spec)
}

// Config 是发布 Agent 的运行参数，全部来自服务端环境变量。
type Config struct {
	Enabled       bool
	MCPURL        string
	ServerName    string
	TokenEnvVar   string
	WorkRoot      string
	CodexPath     string
	Model         string
	Effort        string
	Sandbox       string
	Compatibility string
	Timeout       time.Duration
	// ToolTimeout 是单次 MCP 工具调用的上限。release_import / github_release_sync
	// 一次要传完整制品（Syntropy 全量约 2.4 GB），4 分钟根本不够；导入本身按
	// 「同一 cell 摘要一致就跳过」可续传，超时后用同样的参数再调即可。
	ToolTimeout   time.Duration
	// MaxSessions 限制同时运行的发布 Agent 会话数；0 表示不限制。
	MaxSessions      int
	TokenTTL         time.Duration
	BaseInstructions string
	// ModelEnv 返回项目模型供应商的环境变量（OPENAI_BASE_URL / OPENAI_API_KEY）。
	ModelEnv func(ctx context.Context, projectID string) []string
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.ServerName) == "" {
		c.ServerName = "smartpipe_release"
	}
	if strings.TrimSpace(c.TokenEnvVar) == "" {
		c.TokenEnvVar = "SMARTPIPE_RELEASE_MCP_TOKEN"
	}
	if strings.TrimSpace(c.Sandbox) == "" {
		c.Sandbox = "read-only"
	}
	if c.TokenTTL <= 0 {
		// 0 表示不限制会话时长；令牌仍然需要一个期限，给足一天。
		if c.Timeout <= 0 {
			c.TokenTTL = 24 * time.Hour
		} else {
			c.TokenTTL = c.Timeout + 30*time.Minute
		}
	}
	return c
}

// Launcher 负责创建会话、签发短期 token 并驱动 Codex。
type Launcher struct {
	repo   Repository
	runner Runner
	cfg    Config
	now    func() time.Time

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// New 构造 Launcher。
func New(repo Repository, runner Runner, cfg Config) *Launcher {
	return &Launcher{
		repo: repo, runner: runner, cfg: cfg.withDefaults(),
		now:     func() time.Time { return time.Now().UTC() },
		running: map[string]context.CancelFunc{},
	}
}

// Enabled 报告服务端是否允许启动发布 Agent。
func (l *Launcher) Enabled() bool { return l != nil && l.cfg.Enabled }

// Options 是单次启动的可选覆盖项。
type Options struct {
	Scopes []string
	Model  string
	Effort string
	Actor  string
}

// Launch 为一个运行启动发布 Agent。它立即返回会话记录，实际执行在后台进行。
func (l *Launcher) Launch(ctx context.Context, bundle *releasev2.RunBundle, opts Options) (*releasev2.AgentSession, error) {
	if !l.Enabled() {
		return nil, ErrDisabled
	}
	if bundle == nil || bundle.Run == nil {
		return nil, fmt.Errorf("%w: run is required", releasev2.ErrInvalid)
	}
	if strings.TrimSpace(l.cfg.MCPURL) == "" {
		return nil, fmt.Errorf("%w: MCP URL 未配置", releasev2.ErrInvalid)
	}
	run := bundle.Run
	if !agentStatusAllowed(run.Status) {
		return nil, fmt.Errorf("%w: 运行状态 %s 不能交给 Agent 接管", releasev2.ErrState, run.Status)
	}
	scopes, err := releasev2.ValidateAgentScopes(opts.Scopes)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	if _, busy := l.running[run.ID]; busy {
		l.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	if l.cfg.MaxSessions > 0 && len(l.running) >= l.cfg.MaxSessions {
		l.mu.Unlock()
		return nil, ErrLimit
	}
	// 预留占位，避免同一运行被并发点两次。
	l.running[run.ID] = func() {}
	l.mu.Unlock()
	release := func() {
		l.mu.Lock()
		delete(l.running, run.ID)
		l.mu.Unlock()
	}

	raw, token, err := releasev2.NewAgentToken(run.ID, run.ProjectID, scopes, l.cfg.TokenTTL, l.now())
	if err != nil {
		release()
		return nil, err
	}
	if err := l.repo.CreateReleaseV2Token(ctx, token); err != nil {
		release()
		return nil, err
	}

	model := firstNonEmpty(opts.Model, l.cfg.Model)
	session := &releasev2.AgentSession{
		ID: releasev2.NewAgentSessionID(), RunID: run.ID, ProjectID: run.ProjectID,
		Status: releasev2.AgentSessionStarting, Actor: firstNonEmpty(opts.Actor, "admin"),
		Model: model, TokenID: token.ID,
	}
	if err := l.repo.CreateAgentSession(ctx, session); err != nil {
		release()
		return nil, err
	}

	workDir := filepath.Join(l.cfg.WorkRoot, run.ID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		l.finish(ctx, session, releasev2.AgentSessionFailed, "工作目录不可创建", err.Error())
		release()
		return nil, err
	}

	runCtx := context.Background()
	var cancel context.CancelFunc = func() {}
	if l.cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(context.Background(), l.cfg.Timeout)
	}
	l.mu.Lock()
	l.running[run.ID] = cancel
	l.mu.Unlock()

	effort := firstNonEmpty(opts.Effort, l.cfg.Effort)
	go func() {
		defer release()
		defer cancel()
		l.execute(runCtx, bundle, session, raw, workDir, effort)
	}()
	_ = l.appendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "release-agent", Kind: "agent.session",
		Summary: "发布 Agent 会话已创建",
		Detail:  map[string]any{"session_id": session.ID, "model": model, "scopes": scopes},
	})
	return session, nil
}

// Cancel 中断某个运行的发布 Agent；返回是否确实有活跃会话被取消。
func (l *Launcher) Cancel(runID string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	cancel, ok := l.running[runID]
	l.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// Running 报告某运行是否有活跃会话。
func (l *Launcher) Running(runID string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.running[runID]
	return ok
}

func (l *Launcher) execute(ctx context.Context, bundle *releasev2.RunBundle, session *releasev2.AgentSession, rawToken, workDir, effort string) {
	run := bundle.Run
	stderr := &cappedBuffer{max: 32 << 10}
	var threadRecorded bool
	spec := codexrunner.Spec{
		WorkDir:          workDir,
		Prompt:           BuildPrompt(bundle, l.cfg.MCPURL, l.cfg.ServerName),
		CodexPath:        l.cfg.CodexPath,
		Model:            session.Model,
		Effort:           effort,
		BaseInstructions: firstNonEmpty(l.cfg.BaseInstructions, BaseInstructions),
		Sandbox:          l.cfg.Sandbox,
		Compatibility:    l.cfg.Compatibility,
		Timeout:          l.cfg.Timeout,
		LogWriter:        stderr,
		Env:              l.agentEnv(ctx, run.ProjectID, rawToken),
		ConfigOverrides:  l.mcpOverrides(),
		KeepResponse:     true,
		// 续用上一次会话的线程：构建常常比一轮会话长，重开新线程会让 Agent
		// 忘掉已经排查过的环境和已经下过的结论。
		ResumeThreadID: strings.TrimSpace(run.AgentThreadID),
		PersistThread:  true,
		// 线程一建立就落库，不能等本轮结束：attention 续跑的前提是上一轮
		// 进程被杀，等到最后再写就永远写不进去。
		OnThread: func(threadID string, resumed bool) {
			threadRecorded = l.registerThread(session, threadID, resumed)
		},
		OnEvent: func(ev codexrunner.Event) {
			l.projectEvent(run.ID, session, ev)
		},
	}
	session.Status = releasev2.AgentSessionRunning
	_ = l.repo.UpdateAgentSession(context.Background(), session)
	_ = l.appendEvent(context.Background(), releasev2.Event{
		RunID: run.ID, Actor: "release-agent", Kind: "agent.started",
		Summary: "发布 Agent 开始执行", Detail: map[string]any{"session_id": session.ID},
	})

	res, err := l.runner.Run(ctx, spec)
	now := l.now()
	if session.FinishedAt == nil {
		session.FinishedAt = &now
	}
	if res != nil && strings.TrimSpace(res.ThreadID) != "" {
		session.ThreadID = res.ThreadID
		session.TurnID = res.TurnID
		if !threadRecorded {
			_ = l.registerThread(session, res.ThreadID, res.Resumed)
		}
	}
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)):
		l.finish(context.Background(), session, releasev2.AgentSessionCancelled, "发布 Agent 已取消", "")
		l.handBackRun(session.RunID, "agent cancelled")
	case err != nil:
		l.finish(context.Background(), session, releasev2.AgentSessionFailed, "发布 Agent 异常退出", l.failureDetail(err.Error(), stderr, session))
		l.handBackRun(session.RunID, "agent exited: "+err.Error())
	case res != nil && isFailureStatus(res.Status):
		l.finish(context.Background(), session, releasev2.AgentSessionFailed, "发布 Agent 结束状态 "+res.Status,
			l.failureDetail(firstNonEmpty(res.FinalResponse, stderr.String()), stderr, session))
		l.handBackRun(session.RunID, "agent finished with status "+res.Status)
	default:
		summary := "发布 Agent 本轮结束"
		if res != nil {
			if text := strings.TrimSpace(Redact(res.FinalResponse)); text != "" {
				summary = limit(text, 2000)
			}
		}
		l.finish(context.Background(), session, releasev2.AgentSessionFinished, summary, "")
	}
}

// registerThread 把本轮使用的 Codex 线程登记到运行上：会话表和运行表都要写，
// 这样进程被杀后 attention worker 还能按同一个线程续跑。返回是否写成功。
func (l *Launcher) registerThread(session *releasev2.AgentSession, threadID string, resumed bool) bool {
	threadID = strings.TrimSpace(threadID)
	if l == nil || session == nil || threadID == "" {
		return false
	}
	session.ThreadID = threadID
	if l.repo != nil {
		_ = l.repo.SetReleaseV2RunAgentThreadID(context.Background(), session.RunID, threadID)
	}
	_ = l.appendEvent(context.Background(), releasev2.Event{
		RunID: session.RunID, Actor: "release-agent", Kind: "agent.thread",
		Summary: "发布 Agent 线程已登记，后续会话续用",
		Detail:  map[string]any{"session_id": session.ID, "thread_id": threadID, "resumed": resumed},
	})
	return true
}

// handBackRun 在 Agent 会话异常结束时把运行交回 needs_attention，让
// attention worker 重新接管。已结束或已取消的运行会被 SQL 条件排除。
func (l *Launcher) handBackRun(runID, reason string) {
	if l == nil || l.repo == nil || strings.TrimSpace(runID) == "" {
		return
	}
	_, _ = l.repo.MarkReleaseV2RunNeedsAttention(context.Background(), runID, reason)
}

func (l *Launcher) failureDetail(message string, stderr *cappedBuffer, session *releasev2.AgentSession) string {
	detail := Redact(message)
	if text := strings.TrimSpace(stderr.String()); text != "" {
		_ = l.appendEvent(context.Background(), releasev2.Event{
			RunID: session.RunID, Actor: "release-agent", Kind: "agent.stderr",
			Summary: "Codex stderr", Detail: map[string]any{"session_id": session.ID, "stderr": limit(Redact(text), 8000)},
		})
		if !strings.Contains(detail, text) {
			detail = strings.TrimSpace(detail + "\n" + Redact(text))
		}
	}
	return limit(detail, 4000)
}

// cappedBuffer keeps the last diagnostics without letting a noisy child process
// grow the agent's memory without bound.
type cappedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	written := len(p)
	if c.max <= 0 {
		c.max = 32 << 10
	}
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = c.buf.Write(p)
	}
	return written, nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (l *Launcher) finish(ctx context.Context, session *releasev2.AgentSession, status releasev2.AgentSessionStatus, summary, lastError string) {
	now := l.now()
	if session.FinishedAt == nil {
		session.FinishedAt = &now
	}
	session.Status = status
	if summary != "" {
		session.Summary = limit(summary, 2000)
	}
	if lastError != "" {
		session.LastError = limit(lastError, 2000)
	}
	// 会话结束即吊销本次运行签发的短期 token，减少凭证外泄窗口。
	if strings.TrimSpace(session.TokenID) != "" {
		_ = l.repo.RevokeReleaseV2Token(ctx, session.TokenID)
	}
	_ = l.repo.UpdateAgentSession(ctx, session)
	_ = l.appendEvent(ctx, releasev2.Event{
		RunID: session.RunID, Actor: "release-agent", Kind: "agent." + string(status),
		Summary: firstNonEmpty(session.Summary, string(status)),
		Detail:  map[string]any{"session_id": session.ID, "status": status},
	})
}

func (l *Launcher) agentEnv(ctx context.Context, projectID, rawToken string) []string {
	env := []string{l.cfg.TokenEnvVar + "=" + rawToken}
	if l.cfg.ModelEnv != nil {
		env = append(env, l.cfg.ModelEnv(ctx, projectID)...)
	}
	return env
}

func (l *Launcher) mcpOverrides() []string {
	server := l.cfg.ServerName
	overrides := []string{
		fmt.Sprintf("mcp_servers.%s.url=%q", server, strings.TrimSpace(l.cfg.MCPURL)),
		fmt.Sprintf("mcp_servers.%s.bearer_token_env_var=%q", server, l.cfg.TokenEnvVar),
		fmt.Sprintf("mcp_servers.%s.startup_timeout_sec=60", server),
		// Codex 对没有 readOnlyHint 注解的 MCP 工具默认要求审批；本链路固定
		// approval_policy=never，不显式设成 approve 会在客户端直接拒绝调用。
		fmt.Sprintf("mcp_servers.%s.default_tools_approval_mode=%q", server, "approve"),
	}
	// Codex 对 MCP 工具自带一个很短的默认超时；release_import 要把整包制品灌进
	// 多个升级服务，任何小固定值都会在慢链路上砍断可用的上传。ToolTimeout<=0
	// 表示不限制，这里给一个足够大的值（7 天）而不是省略配置。
	toolTimeout := l.cfg.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 7 * 24 * time.Hour
	}
	overrides = append(overrides, fmt.Sprintf("mcp_servers.%s.tool_timeout_sec=%d", server, int(toolTimeout/time.Second)))
	return overrides
}

func (l *Launcher) appendEvent(ctx context.Context, ev releasev2.Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return l.repo.AppendEvent(ctx, ev)
}

// projectEvent 把 Codex 通知投影成运行事件，供控制台展示。
func (l *Launcher) projectEvent(runID string, session *releasev2.AgentSession, ev codexrunner.Event) {
	var params map[string]any
	if len(ev.Params) > 0 {
		if err := json.Unmarshal(ev.Params, &params); err != nil {
			return
		}
	}
	if params == nil {
		params = map[string]any{}
	}
	ctx := context.Background()
	switch ev.Method {
	case "item/completed", "item/started":
		item, _ := params["item"].(map[string]any)
		if item == nil {
			return
		}
		switch stringAny(item["type"]) {
		case "mcpToolCall", "dynamicToolCall":
			tool := firstNonEmpty(stringAny(item["tool"]), stringAny(item["name"]))
			status := firstNonEmpty(stringAny(item["status"]), "running")
			args := limit(Redact(compactJSON(item["arguments"])), 2000)
			result := limit(Redact(firstNonEmpty(
				stringAny(item["result"]),
				stringAny(item["output"]),
				nestedMessage(item["error"]),
				toolResultText(item["result"]),
			)), 4000)
			_ = l.appendEvent(ctx, releasev2.Event{
				RunID: runID, Actor: "release-agent", Kind: "agent.tool",
				Summary: limit(fmt.Sprintf("%s %s", tool, status), 200),
				Detail: map[string]any{
					"session_id": session.ID, "tool": tool, "status": status,
					"arguments": args, "result": result,
				},
			})
		case "agentMessage":
			text := strings.TrimSpace(Redact(stringAny(item["text"])))
			if text == "" {
				return
			}
			_ = l.appendEvent(ctx, releasev2.Event{
				RunID: runID, Actor: "release-agent", Kind: "agent.message",
				Summary: limit(text, 400),
				Detail:  map[string]any{"session_id": session.ID, "text": limit(text, 8000)},
			})
		}
	case "turn/completed":
		if id := stringAny(params["turnId"]); id != "" {
			session.TurnID = id
		}
	case "error":
		errObj, _ := params["error"].(map[string]any)
		msg := Redact(stringAny(errObj["message"]))
		if msg == "" {
			return
		}
		session.LastError = limit(msg, 1000)
		_ = l.appendEvent(ctx, releasev2.Event{
			RunID: runID, Actor: "release-agent", Kind: "agent.error",
			Summary: limit(msg, 400), Detail: map[string]any{"session_id": session.ID},
		})
	}
}

func isFailureStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "cancelled", "canceled", "interrupted", "timed_out", "timeout":
		return true
	}
	return false
}

func compactJSON(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func stringAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprintf("%v", typed)
	default:
		return ""
	}
}

func nestedMessage(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if msg := firstNonEmpty(stringAny(typed["message"]), stringAny(typed["text"]), stringAny(typed["error"])); msg != "" {
			return msg
		}
		return compactJSON(typed)
	default:
		if value != nil {
			return compactJSON(value)
		}
		return ""
	}
}

func toolResultText(value any) string {
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	var parts []string
	if content, ok := obj["content"].([]any); ok {
		for _, entry := range content {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if text := stringAny(item["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if text := stringAny(obj["text"]); text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}

func limit(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !isRuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// agentStatusAllowed 只允许把尚未收口的运行交给发布 Agent。
func agentStatusAllowed(status releasev2.RunStatus) bool {
	switch status {
	case releasev2.RunQueued, releasev2.RunPreparing, releasev2.RunBuilding,
		releasev2.RunVerifying, releasev2.RunArtifactsReady, releasev2.RunStaged,
		releasev2.RunNeedsAttention, releasev2.RunRecovering, releasev2.RunPublishing,
		// 渠道已推进但 GitHub 失败时运行停在 partially_published；这正是 Agent
		// 需要接手续跑的场景，不能把它挡在门外。
		releasev2.RunPartiallyPublished:
		return true
	}
	return false
}
