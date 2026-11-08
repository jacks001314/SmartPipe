package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/codexrunner"
)

// ErrUnbound 表示群没有绑定项目。
var ErrUnbound = errors.New("scheduler: chat is not bound to a project")

// Runner 执行一轮 Codex 会话；生产实现直接调用 codexrunner.Run。
type Runner interface {
	Run(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error)
}

// RunnerFunc 让函数满足 Runner。
type RunnerFunc func(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error)

// Run 实现 Runner。
func (f RunnerFunc) Run(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error) {
	return f(ctx, spec)
}

// Differ 提取工作区改动。
type Differ interface {
	Diff(ctx context.Context, workDir, gitBin string, exclude ...string) (string, error)
}

// DifferFunc 让函数满足 Differ。
type DifferFunc func(ctx context.Context, workDir, gitBin string, exclude ...string) (string, error)

// Diff 实现 Differ。
func (f DifferFunc) Diff(ctx context.Context, workDir, gitBin string, exclude ...string) (string, error) {
	return f(ctx, workDir, gitBin, exclude...)
}

// Tracker 访问 GitHub（经 admin-api 门禁）。
type Tracker interface {
	CreateIssue(ctx context.Context, projectID, title, body string, labels []string) (Issue, error)
	GetIssue(ctx context.Context, projectID string, number int) (Issue, error)
	Comment(ctx context.Context, projectID string, number int, body string) error
	ListComments(ctx context.Context, projectID string, number int) ([]Comment, error)
	SearchIssues(ctx context.Context, projectID, query string) ([]Issue, error)
	CreatePR(ctx context.Context, projectID string, issue int, head, base, title, body string) (PullRequest, error)
}

// Notifier 把文本发回蓝信群。
type Notifier interface {
	SendGroup(ctx context.Context, botID, chatID, replyTo, text string) error
}

// Resolver 由群/项目 ID 找到项目。
type Resolver interface {
	ByChat(ctx context.Context, chatID string) (*Project, error)
	ByID(ctx context.Context, projectID string) (*Project, error)
}

// Workspace 管理每个 Issue 的 git worktree。
type Workspace interface {
	Prepare(ctx context.Context, p *Project, issue int) (dir, branch string, err error)
	Cleanup(ctx context.Context, p *Project, issue int) error
}

// Verifier 在干净副本上验证补丁；workdir 用于取其基线提交。
type Verifier interface {
	Verify(ctx context.Context, p *Project, workdir, patch string) (ok bool, report string, err error)
}

// VCS 通过 admin-api 门禁提交并推送分支。
type VCS interface {
	CommitPush(ctx context.Context, p *Project, w *Worker, message, patch string) (commit string, err error)
}

// Config 是调度器运行参数。
type Config struct {
	MaxGlobal       int
	MaxPerProject   int
	TurnTimeout     time.Duration
	PollInterval    time.Duration
	VerifyAttempts  int
	MaxAttempts     int
	Model           string
	Effort          string
	CodexPath       string
	ChatHistorySize int
	// ReconcileInterval 定期核对 Issue 是否已关闭（默认 5 分钟）。
	ReconcileInterval time.Duration
}

// Deps 是全部外部依赖，便于替换与测试。
type Deps struct {
	Repo      Repo
	Runner    Runner
	Differ    Differ
	Tracker   Tracker
	Notifier  Notifier
	Resolver  Resolver
	Workspace Workspace
	Verifier  Verifier
	VCS       VCS
	// ModelEnv 可选：按项目供应商返回 OPENAI_BASE_URL / OPENAI_API_KEY 等环境变量。
	ModelEnv func(ctx context.Context, projectID, providerID string) ([]string, error)
}

// Scheduler 是调度器核心。
type Scheduler struct {
	cfg  Config
	repo Repo

	runner    Runner
	differ    Differ
	tracker   Tracker
	notifier  Notifier
	resolver  Resolver
	workspace Workspace
	verifier  Verifier
	vcs       VCS
	modelEnv  func(ctx context.Context, projectID, providerID string) ([]string, error)

	wake chan struct{}
	now  func() time.Time

	mu      sync.Mutex
	running sync.WaitGroup
}

// New 校验依赖并构造调度器。
func New(cfg Config, deps Deps) (*Scheduler, error) {
	if deps.Repo == nil || deps.Runner == nil || deps.Tracker == nil || deps.Notifier == nil || deps.Resolver == nil {
		return nil, errors.New("scheduler: repo/runner/tracker/notifier/resolver are required")
	}
	if cfg.MaxGlobal <= 0 {
		cfg.MaxGlobal = 3
	}
	if cfg.MaxPerProject <= 0 {
		cfg.MaxPerProject = 2
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = 30 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.VerifyAttempts <= 0 {
		cfg.VerifyAttempts = 2
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.ChatHistorySize <= 0 {
		cfg.ChatHistorySize = 12
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = 5 * time.Minute
	}
	return &Scheduler{
		cfg:       cfg,
		repo:      deps.Repo,
		runner:    deps.Runner,
		differ:    deps.Differ,
		tracker:   deps.Tracker,
		notifier:  deps.Notifier,
		resolver:  deps.Resolver,
		workspace: deps.Workspace,
		verifier:  deps.Verifier,
		vcs:       deps.VCS,
		modelEnv:  deps.ModelEnv,
		wake:      make(chan struct{}, 1),
		now:       time.Now,
	}, nil
}

// Wait 等待当前已启动的轮次结束（测试与优雅退出用）。
func (s *Scheduler) Wait() { s.running.Wait() }

// Wake 立即触发一次调度（新任务入队后调用）。
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run 持续领取排队任务，直到 ctx 结束。
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	reconcile := time.NewTicker(s.cfg.ReconcileInterval)
	defer reconcile.Stop()
	for {
		if err := s.DispatchOnce(ctx); err != nil {
			log.Printf("[scheduler] dispatch: %v", err)
		}
		select {
		case <-ctx.Done():
			s.running.Wait()
			return ctx.Err()
		case <-ticker.C:
		case <-reconcile.C:
			if err := s.Reconcile(ctx); err != nil {
				log.Printf("[scheduler] reconcile: %v", err)
			}
		case <-s.wake:
		}
	}
}

// Reconcile 补齐漏掉的 GitHub 事件：Issue 已关闭但 worker 还挂着时直接收尾。
// webhook 可能因为网络或部署重启丢失，这一步保证 worker 不会永远占着名额。
func (s *Scheduler) Reconcile(ctx context.Context) error {
	workers, err := s.repo.ListWorkers(ctx)
	if err != nil {
		return err
	}
	for _, w := range workers {
		switch w.State {
		case StateAwaitingReview, StateAwaitingInput, StateBlocked, StateFailed:
		default:
			continue
		}
		issue, err := s.tracker.GetIssue(ctx, w.ProjectID, w.Issue)
		if err != nil {
			continue
		}
		if strings.EqualFold(issue.State, "closed") {
			s.closeWorker(ctx, w, closeReason(w))
		}
	}
	return nil
}

func closeReason(w *Worker) string {
	if w.PRNumber > 0 {
		return fmt.Sprintf("PR #%d 已合并", w.PRNumber)
	}
	return "Issue 已关闭"
}

// DispatchOnce 在名额允许时领取并启动排队任务（同步等待本轮启动完成）。
func (s *Scheduler) DispatchOnce(ctx context.Context) error {
	for {
		w, err := s.repo.ClaimNext(ctx, s.cfg.MaxGlobal, s.cfg.MaxPerProject)
		if err != nil {
			return err
		}
		if w == nil {
			return nil
		}
		s.mu.Lock()
		s.running.Add(1)
		s.mu.Unlock()
		go func(w *Worker) {
			defer s.running.Done()
			runCtx, cancel := context.WithTimeout(context.Background(), s.cfg.TurnTimeout*4)
			defer cancel()
			if err := s.execute(runCtx, w, ""); err != nil {
				s.failWorker(runCtx, w, err)
			}
			s.Wake()
		}(w)
	}
}

// HandleMessage 处理一条蓝信消息：问答、建单或 Issue 补充。
func (s *Scheduler) HandleMessage(ctx context.Context, msg Inbound) error {
	if strings.TrimSpace(msg.PlatformMessageID) != "" {
		if link, err := s.repo.GetLink(ctx, msg.PlatformMessageID); err == nil && link != nil {
			return nil // 幂等：同一条消息只处理一次
		}
	}
	p, err := s.resolver.ByChat(ctx, msg.ChatID())
	if err != nil {
		_ = s.notify(ctx, "", msg.ChatID(), "", "这个群还没有绑定项目，请管理员在配置台完成绑定。")
		return ErrUnbound
	}
	history, _ := s.repo.RecentChat(ctx, msg.ChatID(), s.cfg.ChatHistorySize)
	referenced := referencedIssue(msg.Text)
	_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "user", Body: msg.Text})

	decision, runErr := s.runChat(ctx, p, history, msg, referenced)
	if runErr != nil {
		log.Printf("[scheduler] chat worker failed project=%s chat=%s: %v", p.ID, msg.ChatID(), runErr)
		_ = s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, "抱歉，这次处理失败了，稍后再试或直接把问题贴到 Issue 里。")
		return runErr
	}
	if err := s.applyChatDecision(ctx, p, msg, decision, referenced); err != nil {
		return err
	}
	_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "assistant", Body: decisionSummary(decision)})
	_ = s.repo.PutLink(ctx, Link{PlatformMessageID: msg.PlatformMessageID, ProjectID: p.ID, Issue: decision.Issue, ChatID: msg.ChatID()})
	return nil
}

func decisionSummary(d *ChatDecision) string {
	if d.Action == "issue" {
		return "已建单：" + d.Title
	}
	if d.Action == "issue_update" {
		return fmt.Sprintf("已补充到 Issue #%d", d.Issue)
	}
	return d.Reply
}

func (s *Scheduler) runChat(ctx context.Context, p *Project, history []ChatMessage, msg Inbound, referenced int) (*ChatDecision, error) {
	spec := codexrunner.Spec{
		WorkDir:          chatWorkDir(p),
		Env:              s.resolveModelEnv(ctx, p),
		Prompt:           buildChatPrompt(p, history, msg, referenced),
		CodexPath:        s.cfg.CodexPath,
		Model:            firstNonEmpty(p.Model, s.cfg.Model),
		Effort:           firstNonEmpty(p.Effort, s.cfg.Effort),
		BaseInstructions: ChatSystemPrompt,
		Sandbox:          "read-only",
		Timeout:          s.cfg.TurnTimeout,
		KeepResponse:     true,
	}
	res, err := s.runner.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	return ParseChatDecision(res.FinalResponse)
}

func (s *Scheduler) applyChatDecision(ctx context.Context, p *Project, msg Inbound, d *ChatDecision, referenced int) error {
	switch d.Action {
	case "answer", "need_info":
		return s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, d.Reply)
	case "issue_update":
		issue := d.Issue
		if issue == 0 {
			issue = referenced
		}
		if issue <= 0 {
			return s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, "没能确定要更新哪个 Issue，请在消息里带上 #编号。")
		}
		if err := s.tracker.Comment(ctx, p.ID, issue, botComment(d.Reply)); err != nil {
			return err
		}
		if w, _ := s.repo.ActiveWorker(ctx, p.ID, issue); w != nil {
			s.resume(w, msg.Text)
		} else {
			created, err := s.repo.CreateWorker(ctx, &Worker{ProjectID: p.ID, Issue: issue, ChatID: msg.ChatID(), State: StateQueued})
			if err != nil {
				return err
			}
			if created {
				s.Wake()
			}
		}
		return s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, fmt.Sprintf("已补充到 Issue #%d。", issue))
	case "issue":
		existing, err := s.findDuplicate(ctx, p.ID, d.Title)
		if err != nil {
			log.Printf("[scheduler] dedupe failed: %v", err)
		}
		if existing != nil {
			_ = s.tracker.Comment(ctx, p.ID, existing.Number, botComment("来自蓝信的新反馈（重复问题）：\n\n"+msg.Text))
			if w, _ := s.repo.ActiveWorker(ctx, p.ID, existing.Number); w != nil {
				s.resume(w, msg.Text)
			}
			return s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID,
				fmt.Sprintf("这个问题已经有 Issue #%d，已把新反馈补充进去。\n%s", existing.Number, existing.URL))
		}
		labels := []string{"from-lanxin", "severity/" + d.Severity}
		issue, err := s.tracker.CreateIssue(ctx, p.ID, d.Title, buildIssueBody(msg, d), labels)
		if err != nil {
			return err
		}
		w := &Worker{ProjectID: p.ID, Issue: issue.Number, IssueURL: issue.URL, ChatID: msg.ChatID(), State: StateQueued}
		created, err := s.repo.CreateWorker(ctx, w)
		if err != nil {
			return err
		}
		if created {
			s.Wake()
		}
		_ = s.repo.PutLink(ctx, Link{PlatformMessageID: msg.PlatformMessageID, ProjectID: p.ID, Issue: issue.Number, ChatID: msg.ChatID()})
		return s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, buildIssueReply(msg, issue))
	}
	return nil
}

func buildIssueBody(msg Inbound, d *ChatDecision) string {
	var b strings.Builder
	b.WriteString(d.Body)
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "- 来源：蓝信 / %s\n- 群：%s（%s）\n- 发言人：%s（%s）\n- 消息 ID：%s\n",
		msg.BotName, msg.GroupName, msg.ChatID(), msg.SenderName, msg.SenderID, msg.PlatformMessageID)
	b.WriteString("\n原始消息：\n\n> " + strings.ReplaceAll(msg.Text, "\n", "\n> "))
	return b.String()
}

func (s *Scheduler) findDuplicate(ctx context.Context, projectID, title string) (*Issue, error) {
	issues, err := s.tracker.SearchIssues(ctx, projectID, title)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(title))
	for i := range issues {
		if strings.ToLower(strings.TrimSpace(issues[i].Title)) == want && !strings.EqualFold(issues[i].State, "closed") {
			return &issues[i], nil
		}
	}
	return nil, nil
}

// HandleIssueEvent 处理 GitHub 事件（issue_comment / issues）。
func (s *Scheduler) HandleIssueEvent(ctx context.Context, ev IssueEvent) error {
	p, err := s.resolver.ByID(ctx, ev.ProjectID)
	if err != nil {
		return err
	}
	if ev.Event == "issues" {
		switch ev.Action {
		case "closed":
			w, _ := s.repo.ActiveWorker(ctx, p.ID, ev.Issue)
			if w != nil {
				s.closeWorker(ctx, w, closeReason(w))
			}
			return nil
		case "reopened":
			w := &Worker{ProjectID: p.ID, Issue: ev.Issue, IssueURL: ev.IssueURL(), State: StateQueued}
			if created, _ := s.repo.CreateWorker(ctx, w); created {
				s.Wake()
			}
			return nil
		}
		return nil
	}
	if ev.Event != "issue_comment" || ev.Action != "created" {
		return nil
	}
	if strings.Contains(ev.Body, botMarker) {
		return nil // 调度器自己发的评论，避免自触发
	}
	w, err := s.repo.ActiveWorker(ctx, p.ID, ev.Issue)
	if err != nil {
		return err
	}
	if w != nil {
		s.mirrorComment(ctx, w, ev)
	}
	if cmd := parseCommand(ev.Body); cmd != "" {
		return s.handleCommand(ctx, p, ev, w, cmd)
	}
	if w == nil {
		w = &Worker{ProjectID: p.ID, Issue: ev.Issue, IssueURL: ev.IssueURL(), State: StateQueued}
		created, err := s.repo.CreateWorker(ctx, w)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		s.Wake()
		return nil
	}
	if w.State == StateClosed || w.State == StateFailed {
		return nil
	}
	s.resume(w, ev.Body)
	return nil
}

func (ev IssueEvent) IssueURL() string {
	if ev.Repo == "" || ev.Issue == 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", strings.TrimPrefix(ev.Repo, "https://github.com/"), ev.Issue)
}

func parseCommand(body string) string {
	line := strings.TrimSpace(body)
	if !strings.HasPrefix(line, "/") {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

func (s *Scheduler) handleCommand(ctx context.Context, p *Project, ev IssueEvent, w *Worker, cmd string) error {
	if w == nil {
		return s.tracker.Comment(ctx, p.ID, ev.Issue, botComment("当前没有进行中的 worker。发送 `/fix` 可以重新排队。"))
	}
	switch cmd {
	case "/status":
		return s.tracker.Comment(ctx, p.ID, ev.Issue, botComment(fmt.Sprintf(
			"状态：`%s`\n分支：`%s`\nPR：#%d\n最近错误：%s", w.State, w.Branch, w.PRNumber, firstNonEmpty(w.LastError, "无"))))
	case "/pause":
		w.State = StateBlocked
		w.LastError = "paused by human"
		return s.repo.UpdateWorker(ctx, w)
	case "/resume", "/retry", "/fix":
		w.State = StateQueued
		w.Attempts = 0
		w.LastError = ""
		if err := s.repo.UpdateWorker(ctx, w); err != nil {
			return err
		}
		s.Wake()
		return nil
	case "/stop":
		s.closeWorker(ctx, w, "人工停止")
		return nil
	default:
		return nil
	}
}

func (s *Scheduler) resume(w *Worker, trigger string) {
	s.mu.Lock()
	s.running.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.running.Done()
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.TurnTimeout*4)
		defer cancel()
		w.State = StatePreparing
		_ = s.repo.UpdateWorker(ctx, w)
		if err := s.execute(ctx, w, trigger); err != nil {
			s.failWorker(ctx, w, err)
		}
		s.Wake()
	}()
}

// execute 跑一轮完整修复：worktree → codex → 验证 → 提交推送 → PR。
func (s *Scheduler) execute(ctx context.Context, w *Worker, trigger string) error {
	p, err := s.resolver.ByID(ctx, w.ProjectID)
	if err != nil {
		return err
	}
	if s.workspace == nil || s.verifier == nil || s.vcs == nil {
		return errors.New("scheduler: workspace/verifier/vcs not configured")
	}
	dir, branch, err := s.workspace.Prepare(ctx, p, w.Issue)
	if err != nil {
		return fmt.Errorf("prepare workspace: %w", err)
	}
	w.Worktree, w.Branch = dir, branch
	issue, err := s.tracker.GetIssue(ctx, p.ID, w.Issue)
	if err != nil {
		return err
	}
	if strings.EqualFold(issue.State, "closed") {
		s.closeWorker(ctx, w, "Issue 已关闭")
		return nil
	}
	comments, _ := s.tracker.ListComments(ctx, p.ID, w.Issue)

	var report, patch string
	ok := false
	for attempt := 0; attempt < s.cfg.VerifyAttempts; attempt++ {
		w.State = StateCoding
		if err := s.repo.UpdateWorker(ctx, w); err != nil {
			return err
		}
		res, err := s.runner.Run(ctx, codexrunner.Spec{
			WorkDir:          dir,
			Env:              s.resolveModelEnv(ctx, p),
			Prompt:           buildFixPrompt(p, issue, comments, trigger, report),
			CodexPath:        s.cfg.CodexPath,
			Model:            firstNonEmpty(p.Model, s.cfg.Model),
			Effort:           firstNonEmpty(p.Effort, s.cfg.Effort),
			BaseInstructions: FixSystemPrompt,
			Timeout:          s.cfg.TurnTimeout,
			KeepResponse:     true,
		})
		if err != nil {
			return fmt.Errorf("codex: %w", err)
		}
		patch, err = s.differ.Diff(ctx, dir, "")
		if err != nil {
			return fmt.Errorf("diff: %w", err)
		}
		if strings.TrimSpace(patch) == "" {
			w.State = StateAwaitingInput
			w.LastError = ""
			_ = s.repo.UpdateWorker(ctx, w)
			_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(
				"这一轮没有产生代码改动。需要补充信息或确认方案：\n\n"+clamp(res.FinalResponse, 3000)))
			s.notifyIssue(ctx, p, w, "需要你补充信息："+clamp(res.FinalResponse, 300))
			return nil
		}
		w.State = StateVerifying
		_ = s.repo.UpdateWorker(ctx, w)
		ok, report, err = s.verifier.Verify(ctx, p, dir, patch)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		if ok {
			break
		}
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment("验证未通过，正在带日志重试：\n\n```\n"+clamp(report, 3000)+"\n```"))
	}
	if !ok {
		w.State = StateBlocked
		w.LastError = "verification failed"
		_ = s.repo.UpdateWorker(ctx, w)
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment("连续验证失败，已暂停。\n\n```\n"+clamp(report, 4000)+"\n```"))
		s.notifyIssue(ctx, p, w, "验证连续失败，需要人工确认。")
		return nil
	}

	w.State = StatePushing
	_ = s.repo.UpdateWorker(ctx, w)
	commit, err := s.vcs.CommitPush(ctx, p, w, commitMessage(issue), patch)
	if err != nil {
		return fmt.Errorf("commit/push: %w", err)
	}
	if w.PRNumber == 0 {
		pr, err := s.tracker.CreatePR(ctx, p.ID, w.Issue, branch, firstNonEmpty(p.DefaultBase, "main"),
			prTitle(issue), prBody(issue, commit))
		if err != nil {
			return fmt.Errorf("create pr: %w", err)
		}
		w.PRNumber = pr.Number
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(fmt.Sprintf(
			"已提交 `%s` 并创建 PR #%d（验证通过）。\n%s", shortSHA(commit), pr.Number, pr.URL)))
		s.notifyIssue(ctx, p, w, fmt.Sprintf("修复完成，PR #%d 等待 review：%s", pr.Number, pr.URL))
	} else {
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(fmt.Sprintf(
			"已追加提交 `%s` 到 PR #%d。", shortSHA(commit), w.PRNumber)))
		s.notifyIssue(ctx, p, w, fmt.Sprintf("已按你的意见更新 PR #%d。", w.PRNumber))
	}
	w.State = StateAwaitingReview
	w.LastError = ""
	return s.repo.UpdateWorker(ctx, w)
}

func commitMessage(issue Issue) string {
	return fmt.Sprintf("fix: %s (#%d)", clamp(strings.ReplaceAll(issue.Title, "\n", " "), 60), issue.Number)
}

func prTitle(issue Issue) string {
	return fmt.Sprintf("fix: %s (#%d)", clamp(strings.ReplaceAll(issue.Title, "\n", " "), 60), issue.Number)
}

func prBody(issue Issue, commit string) string {
	return fmt.Sprintf("## 变更说明\n\n自动修复 Issue #%d：%s\n\n- 提交：`%s`\n- 验证：patch-verifier 通过\n\nFixes #%d",
		issue.Number, issue.Title, shortSHA(commit), issue.Number)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func (s *Scheduler) failWorker(ctx context.Context, w *Worker, cause error) {
	log.Printf("[scheduler] worker %s (project=%s issue=%d) failed: %v", w.ID, w.ProjectID, w.Issue, cause)
	w.Attempts++
	w.LastError = cause.Error()
	state := StateBlocked
	if w.Attempts < s.cfg.MaxAttempts {
		state = StateQueued
	}
	w.State = state
	_ = s.repo.UpdateWorker(ctx, w)
	if state == StateBlocked {
		_ = s.tracker.Comment(ctx, w.ProjectID, w.Issue, botComment("worker 执行失败，已暂停：\n\n```\n"+clamp(cause.Error(), 2000)+"\n```\n发送 `/retry` 重新尝试。"))
		if p, err := s.resolver.ByID(ctx, w.ProjectID); err == nil {
			s.notifyIssue(ctx, p, w, "执行失败，已在 Issue 里记录原因。")
		}
	}
}

// mirrorComment 把 Issue 上的人工评论同步回发起它的蓝信群，保证用户始终在群里看到进展。
func (s *Scheduler) mirrorComment(ctx context.Context, w *Worker, ev IssueEvent) {
	if strings.TrimSpace(w.ChatID) == "" {
		return
	}
	author := firstNonEmpty(ev.Author, "有人")
	body := clamp(strings.TrimSpace(ev.Body), 300)
	text := fmt.Sprintf("%s 在 Issue #%d 评论：%s", author, ev.Issue, body)
	if url := ev.IssueURL(); url != "" {
		text += "\n" + url
	}
	_ = s.notify(ctx, "", w.ChatID, "", text)
}

func (s *Scheduler) closeWorker(ctx context.Context, w *Worker, reason string) {
	w.State = StateClosed
	_ = s.repo.UpdateWorker(ctx, w)
	if s.workspace != nil {
		if p, err := s.resolver.ByID(ctx, w.ProjectID); err == nil {
			_ = s.workspace.Cleanup(ctx, p, w.Issue)
		}
	}
	if w.ChatID != "" {
		text := fmt.Sprintf("Issue #%d 已结束（%s）。", w.Issue, reason)
		if w.IssueURL != "" {
			text += "\n" + w.IssueURL
		}
		_ = s.notify(ctx, "", w.ChatID, "", text)
	}
}

func (s *Scheduler) notify(ctx context.Context, botID, chatID, replyTo, text string) error {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	if s.notifier == nil {
		return nil
	}
	return s.notifier.SendGroup(ctx, botID, chatID, replyTo, text)
}

func (s *Scheduler) notifyIssue(ctx context.Context, p *Project, w *Worker, text string) {
	chatID := w.ChatID
	if chatID == "" {
		return
	}
	_ = s.notify(ctx, "", chatID, "", fmt.Sprintf("%s（Issue #%d）", text, w.Issue))
}

const botMarker = "<!-- smartpipe:bot -->"

func botComment(body string) string {
	return botMarker + "\n" + body
}

func referencedIssue(text string) int {
	for _, token := range strings.Fields(text) {
		token = strings.TrimSpace(strings.Trim(token, "，。,.()（）<>"))
		if strings.HasPrefix(token, "#") {
			var n int
			if _, err := fmt.Sscanf(token, "#%d", &n); err == nil && n > 0 {
				return n
			}
		}
		// 群里经常直接贴 Issue 链接：https://github.com/owner/repo/issues/42
		if i := strings.LastIndex(token, "/issues/"); i >= 0 {
			if n, err := strconv.Atoi(strings.TrimRight(token[i+len("/issues/"):], "/")); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func chatWorkDir(p *Project) string {
	if p.LocalPath != "" {
		return p.LocalPath
	}
	return "."
}

// resolveModelEnv 读取项目绑定的供应商配置；失败时返回 nil，由 Codex 默认配置兜底。
func (s *Scheduler) resolveModelEnv(ctx context.Context, p *Project) []string {
	if s.modelEnv == nil || strings.TrimSpace(p.ProviderID) == "" {
		return nil
	}
	env, err := s.modelEnv(ctx, p.ID, p.ProviderID)
	if err != nil {
		log.Printf("[scheduler] model env for %s: %v", p.ID, err)
		return nil
	}
	return env
}
