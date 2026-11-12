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
	Enabled          bool
	MCPURL           string
	ServerName       string
	TokenEnvVar      string
	WorkRoot         string
	CodexPath        string
	Model            string
	Effort           string
	Sandbox          string
	Compatibility    string
	Timeout          time.Duration
	// MaxSessions 限制同时运行的发布 Agent 会话数；0 表示不限制。
	MaxSessions int
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
	if c.Timeout <= 0 {
		c.Timeout = 45 * time.Minute
	}
	if c.TokenTTL <= 0 {
		c.TokenTTL = c.Timeout + 30*time.Minute
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

	runCtx, cancel := context.WithTimeout(context.Background(), l.cfg.Timeout)
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
		Env:              l.agentEnv(ctx, run.ProjectID, rawToken),
		ConfigOverrides:  l.mcpOverrides(),
		KeepResponse:     true,
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
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)):
		l.finish(context.Background(), session, releasev2.AgentSessionCancelled, "发布 Agent 已取消", "")
	case err != nil:
		l.finish(context.Background(), session, releasev2.AgentSessionFailed, "发布 Agent 异常退出", Redact(err.Error()))
	case res != nil && isFailureStatus(res.Status):
		l.finish(context.Background(), session, releasev2.AgentSessionFailed, "发布 Agent 结束状态 "+res.Status, Redact(res.FinalResponse))
	default:
		summary := "发布 Agent 本轮结束"
		if res != nil {
			session.ThreadID = res.ThreadID
			session.TurnID = res.TurnID
			if text := strings.TrimSpace(Redact(res.FinalResponse)); text != "" {
				summary = limit(text, 2000)
			}
		}
		l.finish(context.Background(), session, releasev2.AgentSessionFinished, summary, "")
	}
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
	return []string{
		fmt.Sprintf("mcp_servers.%s.url=%q", server, strings.TrimSpace(l.cfg.MCPURL)),
		fmt.Sprintf("mcp_servers.%s.bearer_token_env_var=%q", server, l.cfg.TokenEnvVar),
		fmt.Sprintf("mcp_servers.%s.tool_timeout_sec=180", server),
		fmt.Sprintf("mcp_servers.%s.startup_timeout_sec=60", server),
	}
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
			result := limit(Redact(firstNonEmpty(stringAny(item["result"]), stringAny(item["output"]))), 4000)
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
		releasev2.RunVerifying, releasev2.RunStaged:
		return true
	}
	return false
}
