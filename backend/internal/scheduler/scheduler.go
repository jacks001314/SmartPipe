package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/codexrunner"
)

// ErrUnbound 表示群没有绑定项目。
var ErrUnbound = errors.New("scheduler: chat is not bound to a project")

// ErrWorkerNotFound 表示 worker 不存在。
var ErrWorkerNotFound = errors.New("scheduler: worker not found")

// ErrActionNotAllowed 表示当前状态不允许执行该动作。
var ErrActionNotAllowed = errors.New("scheduler: action not allowed")

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

// PullMerger 是可选能力：PR 就绪后由控制面直接合入基线分支。
// Tracker 实现它就启用自动合并；测试里的假 tracker 不实现时保持等待 review。
type PullMerger interface {
	MergePR(ctx context.Context, projectID string, issue, pull int, title string) error
}

// RepoSyncer 是可选能力：开跑前把项目本地 clone 拉到远端最新。
// Tracker 实现它就启用；测试里的假 tracker 不实现也不影响。
type RepoSyncer interface {
	SyncRepo(ctx context.Context, projectID string) error
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
	Prepare(ctx context.Context, p *Project, issue int, preserve bool) (dir, branch string, err error)
	Cleanup(ctx context.Context, p *Project, issue int) error
}

// Committer 把工作区里已暂存的改动提交成一次本地 commit。
type Committer interface {
	Commit(ctx context.Context, workDir, message string) (commit string, err error)
}

// VCS 通过 admin-api 门禁推送分支。
type VCS interface {
	Push(ctx context.Context, p *Project, w *Worker) (head string, err error)
}

// Config 是调度器运行参数。
type Config struct {
	MaxGlobal     int
	MaxPerProject int
	TurnTimeout   time.Duration
	PollInterval  time.Duration
	MaxAttempts   int
	Model         string
	Effort        string
	CodexPath     string
	// CodexSandbox 是 Codex 内层沙箱模式；容器部署用 danger-full-access，
	// 由容器本身承担隔离，宿主机部署可以留空走 workspace-write。
	CodexSandbox    string
	ChatHistorySize int
	// ReconcileInterval 定期核对 Issue 是否已关闭（默认 5 分钟）。
	ReconcileInterval time.Duration
	// HeartbeatInterval 是执行期心跳间隔（默认 20 秒）。
	HeartbeatInterval time.Duration
	// HeartbeatTimeout 是判定执行器失联的阈值（默认 2 分钟）。
	HeartbeatTimeout time.Duration
	// OutputDir 保存超过内联阈值的大段 Codex 输出；默认落在系统临时目录，
	// 生产部署应显式指向持久卷。
	OutputDir string
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
	Committer Committer
	VCS       VCS
	// ModelEnv 可选：按项目供应商返回 OPENAI_BASE_URL / OPENAI_API_KEY 等环境变量。
	ModelEnv func(ctx context.Context, projectID, providerID string) ([]string, error)
}

// Scheduler 是调度器核心。
type workerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Scheduler 是调度器核心。
type Scheduler struct {
	cfg       Config
	repo      Repo
	startedAt time.Time

	runner    Runner
	differ    Differ
	tracker   Tracker
	notifier  Notifier
	resolver  Resolver
	workspace Workspace
	committer Committer
	vcs       VCS
	modelEnv  func(ctx context.Context, projectID, providerID string) ([]string, error)

	wake        chan struct{}
	now         func() time.Time
	outputBlobs *outputBlobStore

	mu      sync.Mutex
	running sync.WaitGroup
	stateMu sync.Mutex
	runsMu  sync.Mutex
	runs    map[string]*workerRun
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
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.ChatHistorySize <= 0 {
		cfg.ChatHistorySize = 20
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = 5 * time.Minute
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 20 * time.Second
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 2 * time.Minute
	}
	return &Scheduler{
		cfg:         cfg,
		startedAt:   time.Now(),
		repo:        deps.Repo,
		runner:      deps.Runner,
		differ:      deps.Differ,
		tracker:     deps.Tracker,
		notifier:    deps.Notifier,
		resolver:    deps.Resolver,
		workspace:   deps.Workspace,
		committer:   deps.Committer,
		vcs:         deps.VCS,
		modelEnv:    deps.ModelEnv,
		wake:        make(chan struct{}, 1),
		now:         time.Now,
		runs:        map[string]*workerRun{},
		outputBlobs: newOutputBlobStore(cfg.OutputDir),
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
	if err := s.Recover(ctx); err != nil {
		log.Printf("[scheduler] recover: %v", err)
	}
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
		if w.State == StateClosed || w.State == StateFailed {
			continue
		}
		if w.State.IsActive() && !s.isRunning(w.ID) && staleHeartbeat(w, s.now(), s.cfg.HeartbeatTimeout) {
			fresh, _ := s.repo.GetWorker(ctx, w.ID)
			if fresh == nil {
				continue
			}
			fresh.LastError = "heartbeat lost"
			fresh.BlockedReason = "执行器心跳中断，请重试或停止"
			s.setState(ctx, fresh, StateBlocked, "执行器心跳中断，已转为待人工处理", "scheduler")
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

func staleHeartbeat(w *Worker, now time.Time, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	last := w.UpdatedAt
	if w.HeartbeatAt != nil {
		last = *w.HeartbeatAt
	}
	return !last.IsZero() && now.Sub(last) > timeout
}

// Recover 在调度器启动时收拢上次进程遗留的活跃 worker，避免幽灵任务长期占用并发名额。
func (s *Scheduler) Recover(ctx context.Context) error {
	workers, err := s.repo.ListWorkers(ctx)
	if err != nil {
		return err
	}
	for _, w := range workers {
		if !w.State.IsActive() || !staleHeartbeat(w, s.now(), s.cfg.HeartbeatTimeout) {
			continue
		}
		fresh, err := s.repo.GetWorker(ctx, w.ID)
		if err != nil || fresh == nil {
			continue
		}
		fresh.LastError = "scheduler restarted while worker was " + string(w.State)
		fresh.BlockedReason = "调度器重启，执行已中断，请重试或停止"
		_ = s.setState(ctx, fresh, StateBlocked, "调度器重启，遗留执行已转为待人工处理", "scheduler")
	}
	return nil
}

func closeReason(w *Worker) string {
	if w.PRNumber > 0 {
		return fmt.Sprintf("PR #%d 已合并", w.PRNumber)
	}
	return "Issue 已关闭"
}

// DispatchOnce 在名额允许时领取并启动排队任务。
func (s *Scheduler) DispatchOnce(ctx context.Context) error {
	for {
		w, err := s.repo.ClaimNext(ctx, s.cfg.MaxGlobal, s.cfg.MaxPerProject)
		if err != nil {
			return err
		}
		if w == nil {
			return nil
		}
		s.recordEvent(context.Background(), w, "claim", "", string(w.State), "scheduler", "worker 已领取", nil)
		s.startWorker(w, "")
	}
}

type messageOutcome struct {
	Status   string
	Reason   string
	DraftID  string
	Issue    int
	IssueURL string
}

// HandleMessage 处理一条蓝信消息：问答、建单或 Issue 补充。
func (s *Scheduler) HandleMessage(ctx context.Context, msg Inbound) error {
	if strings.TrimSpace(msg.PlatformMessageID) != "" {
		if link, err := s.repo.GetLink(ctx, msg.PlatformMessageID); err == nil && link != nil {
			return nil // 幂等：同一条消息只处理一次
		}
		if claimed, err := s.repo.ClaimMessage(ctx, msg.PlatformMessageID, msg.BotID, s.now(), messageClaimTTL); err != nil {
			log.Printf("[scheduler] claim message %s: %v", msg.PlatformMessageID, err)
		} else if !claimed {
			log.Printf("[scheduler] message %s already claimed by another bot", msg.PlatformMessageID)
			return nil
		}
	}
	p, err := s.resolver.ByChat(ctx, msg.ChatID())
	if err != nil {
		_ = s.repo.RecordMessage(ctx, messageRecord(msg, "", "unbound", "群未绑定项目"))
		if isDirectMessage(msg) {
			_ = s.notify(ctx, "", msg.ChatID(), "", "这个群还没有绑定项目，请管理员在配置台完成绑定。")
		}
		return ErrUnbound
	}
	_ = s.repo.RecordMessage(ctx, messageRecord(msg, p.ID, "processing", ""))
	history, _ := s.repo.RecentChat(ctx, msg.ChatID(), s.cfg.ChatHistorySize)
	referenced := referencedIssue(msg.Text)
	_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "user", Body: msg.Text})

	if msg.ChatType == "group" && !isDirectMessage(msg) {
		policy := normalizeChatPolicy(p.Policy)
		switch policy.Mode {
		case "smart":
			return s.handleObserved(ctx, p, msg, history, false)
		case "all":
			return s.handleObserved(ctx, p, msg, history, true)
		default:
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "ignored", "at_only：未 @ 机器人，也不是明确命令", 0, "")
			return nil
		}
	}

	if handled, draftErr := s.answerIssueDraft(ctx, p, msg); handled {
		return draftErr
	}
	if handled, controlErr := s.answerWorkerControl(ctx, p, msg, history); handled {
		return controlErr
	}
	if handled, statusErr := s.answerWorkerStatus(ctx, p, msg, history); handled {
		return statusErr
	}
	var activeWorker *Worker
	if referenced > 0 {
		activeWorker, _ = s.repo.ActiveWorker(ctx, p.ID, referenced)
	}

	decision, runErr := s.runChat(ctx, p, history, msg, referenced, activeWorker)
	if runErr != nil {
		log.Printf("[scheduler] chat worker failed project=%s chat=%s: %v", p.ID, msg.ChatID(), runErr)
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "failed", clamp(runErr.Error(), 1000), 0, "")
		_ = s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, "抱歉，这次处理失败了，稍后再试或直接把问题贴到 Issue 里。")
		return runErr
	}
	outcome, err := s.applyChatDecision(ctx, p, msg, decision, referenced)
	if err != nil {
		log.Printf("[scheduler] apply chat decision failed project=%s chat=%s: %v", p.ID, msg.ChatID(), err)
		_ = s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID,
			"抱歉，这次没能完成操作："+shortErr(err)+"。稍后再试，或直接把问题贴到 Issue 里。")
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "failed", clamp(err.Error(), 1000), outcome.Issue, outcome.IssueURL)
		return err
	}
	_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, outcome.Status, outcome.Reason, outcome.Issue, outcome.IssueURL)
	_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "assistant", Body: decisionSummary(decision, outcome)})
	_ = s.repo.PutLink(ctx, Link{PlatformMessageID: msg.PlatformMessageID, ProjectID: p.ID, Issue: outcome.Issue, ChatID: msg.ChatID()})
	return nil
}

func messageRecord(msg Inbound, projectID, status, reason string) MessageRecord {
	return MessageRecord{
		PlatformMessageID: msg.PlatformMessageID, BotID: msg.BotID, BotName: msg.BotName,
		ChatType: msg.ChatType, ChatID: msg.ChatID(), SenderID: msg.SenderID, SenderName: msg.SenderName,
		GroupName: msg.GroupName, MsgType: msg.MsgType, Text: msg.Text, IsAtMe: msg.IsAtMe,
		ProjectID: projectID, Status: status, Reason: reason,
	}
}

func decisionSummary(d *ChatDecision, outcome messageOutcome) string {
	switch d.Action {
	case "issue":
		if outcome.Status == "awaiting_confirmation" {
			return fmt.Sprintf("待确认建单 %s：%s", outcome.DraftID, d.Title)
		}
		if outcome.Status == "issue_updated" {
			return fmt.Sprintf("已补充到已有 Issue #%d：%s", outcome.Issue, d.Title)
		}
		if outcome.Issue > 0 {
			return fmt.Sprintf("已建单 #%d：%s", outcome.Issue, d.Title)
		}
		return "已建单：" + d.Title
	case "issue_update":
		issue := outcome.Issue
		if issue == 0 {
			issue = d.Issue
		}
		return fmt.Sprintf("已补充到 Issue #%d", issue)
	default:
		return d.Reply
	}
}

func (s *Scheduler) runChat(ctx context.Context, p *Project, history []ChatMessage, msg Inbound, referenced int, activeWorker *Worker) (*ChatDecision, error) {
	prompt := buildChatPrompt(p, history, msg, referenced, activeWorker)
	images, listing, cleanup, err := saveChatAttachments(msg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if len(listing) > 0 {
		prompt += "\n\n附件（已经下载到本机，可直接按路径读取）：\n" + strings.Join(listing, "\n")
	}
	spec := codexrunner.Spec{
		WorkDir:          chatWorkDir(p),
		Env:              s.resolveModelEnv(ctx, p),
		Prompt:           prompt,
		Images:           images,
		CodexPath:        s.cfg.CodexPath,
		Model:            firstNonEmpty(p.Model, s.cfg.Model),
		Effort:           firstNonEmpty(p.Effort, s.cfg.Effort),
		BaseInstructions: ChatSystemPrompt,
		Sandbox:          firstNonEmpty(s.cfg.CodexSandbox, "read-only"),
		Timeout:          s.cfg.TurnTimeout,
		KeepResponse:     true,
	}
	res, err := s.runner.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("scheduler: chat worker returned no result")
	}
	if strings.TrimSpace(res.FinalResponse) == "" {
		// 只说“空决策”没法排查，把状态和耗时带出来。
		return nil, fmt.Errorf("scheduler: chat worker returned no output (status=%s duration=%dms)", res.Status, res.DurationMS)
	}
	return ParseChatDecision(res.FinalResponse)
}

func (s *Scheduler) applyChatDecision(ctx context.Context, p *Project, msg Inbound, d *ChatDecision, referenced int) (messageOutcome, error) {
	switch d.Action {
	case "answer", "need_info":
		if err := s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, d.Reply); err != nil {
			return messageOutcome{Status: "failed", Reason: err.Error()}, err
		}
		status := "answered"
		if d.Action == "need_info" {
			status = "need_info"
		}
		return messageOutcome{Status: status, Reason: clamp(d.Reply, 500)}, nil
	case "issue_update":
		issue := d.Issue
		if issue == 0 {
			issue = referenced
		}
		if issue <= 0 {
			_ = s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, "没能确定要更新哪个 Issue，请在消息里带上 #编号。")
			return messageOutcome{Status: "need_info", Reason: "没有识别到 Issue 编号"}, nil
		}
		if err := s.tracker.Comment(ctx, p.ID, issue, botComment(d.Reply)); err != nil {
			return messageOutcome{Status: "failed", Issue: issue, IssueURL: issueURLFor(p, issue)}, err
		}
		if w, _ := s.repo.ActiveWorker(ctx, p.ID, issue); w != nil {
			s.resume(w, msg.Text)
		} else {
			created, err := s.queueWorker(ctx, &Worker{ProjectID: p.ID, Issue: issue, ChatID: msg.ChatID(), State: StateQueued}, "来自蓝信补充，worker 已入队")
			if err != nil {
				return messageOutcome{Status: "failed", Issue: issue, IssueURL: issueURLFor(p, issue)}, err
			}
			if created {
				s.Wake()
			}
		}
		if err := s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, fmt.Sprintf("已补充到 Issue #%d。", issue)); err != nil {
			return messageOutcome{Status: "failed", Issue: issue, IssueURL: issueURLFor(p, issue)}, err
		}
		return messageOutcome{Status: "issue_updated", Reason: clamp(d.Reply, 500), Issue: issue, IssueURL: issueURLFor(p, issue)}, nil
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
			if err := s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID,
				fmt.Sprintf("这个问题已经有 Issue #%d，已把新反馈补充进去。\n%s", existing.Number, existing.URL)); err != nil {
				return messageOutcome{Status: "failed", Issue: existing.Number, IssueURL: existing.URL}, err
			}
			return messageOutcome{Status: "issue_updated", Reason: "重复问题，已补充到已有 Issue", Issue: existing.Number, IssueURL: existing.URL}, nil
		}
		draft, err := s.newIssueDraft(ctx, p, msg, d)
		if err != nil {
			return messageOutcome{Status: "failed"}, err
		}
		// 建单不再等群里确认：草稿落库后立刻建 Issue 并派 worker。
		// 失败时 confirmIssueDraft 已经把原因回给群里，草稿退回 pending，可用“确认建单”重试。
		issue, issueURL, confirmErr := s.confirmIssueDraft(ctx, p, msg, draft)
		if confirmErr != nil {
			return messageOutcome{Status: "failed", Reason: clamp(confirmErr.Error(), 300), DraftID: draft.ID, Issue: issue, IssueURL: issueURL}, nil
		}
		return messageOutcome{Status: "issue_created", Reason: draft.Title, DraftID: draft.ID, Issue: issue, IssueURL: issueURL}, nil
	}
	return messageOutcome{Status: "ignored"}, nil
}

func issueURLFor(p *Project, issue int) string {
	if p == nil || issue <= 0 {
		return ""
	}
	repo := firstNonEmpty(p.IssueRepo, p.Repo)
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.Trim(repo, "/")
	if repo == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, issue)
}

func buildIssueBody(msg Inbound, d *ChatDecision) string {
	var b strings.Builder
	b.WriteString(d.Body)
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "- 来源：蓝信 / %s\n- 群：%s\n- 发言人：%s\n",
		firstNonEmpty(strings.TrimSpace(msg.BotName), "蓝信机器人"),
		firstNonEmpty(strings.TrimSpace(msg.GroupName), "未知群"),
		firstNonEmpty(strings.TrimSpace(msg.SenderName), "未知成员"))
	b.WriteString(fmt.Sprintf("\n<!-- 蓝信元数据：会话ID=%s；发言人ID=%s；消息ID=%s -->\n",
		safeComment(msg.ChatID()), safeComment(msg.SenderID), safeComment(msg.PlatformMessageID)))
	b.WriteString("\n原始消息：\n\n> " + strings.ReplaceAll(msg.Text, "\n", "\n> "))
	return b.String()
}

func safeComment(s string) string {
	s = strings.ReplaceAll(s, "--", "- -")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
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
			w := &Worker{ProjectID: p.ID, Issue: ev.Issue, IssueTitle: ev.Title, IssueURL: ev.IssueURL(), State: StateQueued}
			if created, _ := s.queueWorker(ctx, w, "Issue 重新打开，worker 已入队"); created {
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
		w = &Worker{ProjectID: p.ID, Issue: ev.Issue, IssueTitle: ev.Title, IssueURL: ev.IssueURL(), State: StateQueued}
		created, err := s.queueWorker(ctx, w, "Issue 评论触发 worker 入队")
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
		_, err := s.PauseWorker(ctx, w.ID)
		return err
	case "/resume", "/fix":
		_, err := s.ResumeWorker(ctx, w.ID)
		return err
	case "/retry":
		_, err := s.RetryWorker(ctx, w.ID)
		return err
	case "/stop":
		_, err := s.StopWorker(ctx, w.ID)
		return err
	default:
		return nil
	}
}

func (s *Scheduler) resume(w *Worker, trigger string) {
	s.startWorker(w, trigger)
}

// startWorker 为单个 worker 建立可取消的执行上下文；同一 worker 同时最多一个 turn。
func (s *Scheduler) startWorker(w *Worker, trigger string) bool {
	s.runsMu.Lock()
	if _, exists := s.runs[w.ID]; exists {
		s.runsMu.Unlock()
		s.recordEvent(context.Background(), w, "resume_ignored", string(w.State), string(w.State), "scheduler", "worker 已在执行，忽略重复唤醒", nil)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.TurnTimeout*4)
	done := make(chan struct{})
	s.runs[w.ID] = &workerRun{cancel: cancel, done: done}
	s.runsMu.Unlock()

	s.mu.Lock()
	s.running.Add(1)
	s.mu.Unlock()
	go func() {
		defer func() {
			close(done)
			cancel()
			s.runsMu.Lock()
			delete(s.runs, w.ID)
			s.runsMu.Unlock()
			s.running.Done()
			s.Wake()
		}()
		fresh, err := s.repo.GetWorker(context.Background(), w.ID)
		if err == nil && fresh != nil {
			w = fresh
		}
		if w.State == StatePaused || w.State == StateClosed || w.State == StateFailed {
			return
		}
		if w.State != StatePreparing {
			_ = s.setState(context.Background(), w, StatePreparing, "开始准备执行", "scheduler")
		}
		s.markStarted(context.Background(), w)
		if err := s.execute(ctx, w, trigger); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			fresh, _ := s.repo.GetWorker(context.Background(), w.ID)
			if fresh != nil && (fresh.State == StatePaused || fresh.State == StateClosed) {
				return
			}
			s.failWorker(context.Background(), w, err)
		}
	}()
	return true
}

func (s *Scheduler) isRunning(id string) bool {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	_, ok := s.runs[id]
	return ok
}

func (s *Scheduler) cancelRun(id string) chan struct{} {
	s.runsMu.Lock()
	run := s.runs[id]
	s.runsMu.Unlock()
	if run == nil {
		return nil
	}
	run.cancel()
	return run.done
}

func (s *Scheduler) markStarted(ctx context.Context, w *Worker) {
	now := s.now()
	if w.StartedAt == nil {
		w.StartedAt = &now
	}
	w.Round++
	if w.Round <= 1 {
		s.recordEvent(ctx, w, "round", "", string(w.State), "scheduler", "开始第 1 轮执行", map[string]any{"round": w.Round})
	} else {
		s.recordEvent(ctx, w, "round", "", string(w.State), "scheduler", fmt.Sprintf("开始第 %d 轮执行", w.Round), map[string]any{"round": w.Round})
	}
	_ = s.repo.UpdateWorker(ctx, w)
	s.startHeartbeat(w.ID)
}

func (s *Scheduler) startHeartbeat(id string) {
	go func() {
		ticker := time.NewTicker(s.cfg.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.runsMu.Lock()
				_, alive := s.runs[id]
				s.runsMu.Unlock()
				if !alive {
					return
				}
				_ = s.repo.TouchWorker(context.Background(), id, s.now())
			case <-time.After(s.cfg.TurnTimeout * 4):
				return
			}
		}
	}()
}

func (s *Scheduler) setState(ctx context.Context, w *Worker, next State, summary, actor string) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	from := w.State
	if from == StatePaused && next != StateQueued && next != StateClosed {
		return nil
	}
	if from == StateClosed && next != StateClosed {
		return nil
	}
	if from != next {
		now := s.now()
		w.StateChangedAt = now
		if next == StateClosed || next == StateFailed {
			w.FinishedAt = &now
		}
		if next == StateQueued {
			w.FinishedAt = nil
			w.BlockedReason = ""
			w.QueuedAt = now
		}
	}
	w.State = next
	if err := s.repo.UpdateWorker(ctx, w); err != nil {
		return err
	}
	if from != next {
		s.recordEvent(ctx, w, "state", string(from), string(next), actor, summary, nil)
	}
	return nil
}

func (s *Scheduler) recordEvent(ctx context.Context, w *Worker, kind, from, to, actor, summary string, detail map[string]any) {
	if w == nil || w.ID == "" {
		return
	}
	if actor == "" {
		actor = "scheduler"
	}
	_ = s.repo.AppendWorkerEvent(ctx, WorkerEvent{
		WorkerID: w.ID, ProjectID: w.ProjectID, Issue: w.Issue, At: s.now(),
		Kind: kind, StateFrom: from, StateTo: to, Actor: actor, Summary: summary, Detail: detail,
	})
}

func (s *Scheduler) queueWorker(ctx context.Context, w *Worker, summary string) (bool, error) {
	created, err := s.repo.CreateWorker(ctx, w)
	if err != nil {
		return false, err
	}
	if created {
		s.recordEvent(ctx, w, "state", "", string(w.State), "scheduler", summary, nil)
	}
	return created, nil
}

// Workers 返回全部 worker，并补充项目名、运行态和允许动作。
func (s *Scheduler) Workers(ctx context.Context) ([]*Worker, error) {
	workers, err := s.repo.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}
	s.decorate(ctx, workers)
	return workers, nil
}

// WorkerDetail 返回单个 worker 与事件时间线。
func (s *Scheduler) WorkerDetail(ctx context.Context, id string) (*WorkerDetail, error) {
	w, err := s.repo.GetWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, ErrWorkerNotFound
	}
	s.decorate(ctx, []*Worker{w})
	events, err := s.repo.ListWorkerEvents(ctx, id, 300)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		events = []WorkerEvent{{
			WorkerID: w.ID, ProjectID: w.ProjectID, Issue: w.Issue, At: w.CreatedAt,
			Kind: "state", StateTo: string(w.State), Actor: "scheduler", Summary: "worker 已创建",
		}}
	}
	return &WorkerDetail{Worker: w, Events: events}, nil
}

// Overview 汇总调度器状态、并发配置、状态计数和 worker 列表。
func (s *Scheduler) Overview(ctx context.Context) (*Overview, error) {
	all, err := s.Workers(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	active := make([]*Worker, 0, len(all))
	terminal := make([]*Worker, 0)
	for _, w := range all {
		counts[string(w.State)]++
		if w.State == StateClosed || w.State == StateFailed {
			terminal = append(terminal, w)
		} else {
			active = append(active, w)
		}
	}
	sort.SliceStable(terminal, func(i, j int) bool { return terminal[i].UpdatedAt.After(terminal[j].UpdatedAt) })
	if len(terminal) > 50 {
		terminal = terminal[:50]
	}
	workers := append(active, terminal...)
	return &Overview{
		Service: "smartpipe-scheduler", StartedAt: s.startedAt,
		MaxGlobal: s.cfg.MaxGlobal, MaxPerProject: s.cfg.MaxPerProject,
		Counts: counts, Workers: workers,
	}, nil
}

func (s *Scheduler) decorate(ctx context.Context, workers []*Worker) {
	projects := map[string]*Project{}
	for _, w := range workers {
		if w == nil {
			continue
		}
		w.Running = s.isRunning(w.ID)
		w.AllowedActions = allowedActions(w.State)
		if _, seen := projects[w.ProjectID]; !seen {
			p, err := s.resolver.ByID(ctx, w.ProjectID)
			if err != nil {
				p = nil
			}
			projects[w.ProjectID] = p
		}
		if p := projects[w.ProjectID]; p != nil {
			w.ProjectName = p.Name
		}
	}
}

func allowedActions(state State) []string {
	switch state {
	case StateQueued, StatePreparing, StateCoding, StateVerifying, StatePushing:
		return []string{"pause", "stop"}
	case StatePaused:
		return []string{"resume", "stop"}
	case StateBlocked:
		return []string{"resume", "retry", "stop"}
	case StateAwaitingInput:
		return []string{"resume", "stop"}
	case StateAwaitingReview:
		return []string{"resume", "stop"}
	case StateFailed:
		return []string{"retry"}
	default:
		return nil
	}
}

func (s *Scheduler) loadWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := s.repo.GetWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, ErrWorkerNotFound
	}
	return w, nil
}

// PauseWorker 只暂停指定 worker：取消当前 turn，保留分支和工作区。
func (s *Scheduler) PauseWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := s.loadWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.State == StateClosed || w.State == StateFailed || w.State == StatePaused {
		return w, fmt.Errorf("%w: cannot pause from %s", ErrActionNotAllowed, w.State)
	}
	w.LastError = ""
	w.BlockedReason = "paused by human"
	if err := s.setState(ctx, w, StatePaused, "人工暂停当前 worker", "console"); err != nil {
		return nil, err
	}
	s.cancelRun(id)
	return w, nil
}

// ResumeWorker 把暂停或等待中的 worker 重新放回队列。
func (s *Scheduler) ResumeWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := s.loadWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.isRunning(id) {
		return w, fmt.Errorf("%w: worker is already running", ErrActionNotAllowed)
	}
	switch w.State {
	case StatePaused, StateBlocked, StateAwaitingInput, StateAwaitingReview:
	default:
		return w, fmt.Errorf("%w: cannot resume from %s", ErrActionNotAllowed, w.State)
	}
	w.LastError = ""
	w.BlockedReason = ""
	if err := s.setState(ctx, w, StateQueued, "人工恢复，重新排队", "console"); err != nil {
		return nil, err
	}
	s.Wake()
	return w, nil
}

// RetryWorker 清空尝试次数后重新排队。
func (s *Scheduler) RetryWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := s.loadWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.isRunning(id) {
		return w, fmt.Errorf("%w: worker is already running", ErrActionNotAllowed)
	}
	switch w.State {
	case StateBlocked, StateFailed, StatePaused:
	default:
		return w, fmt.Errorf("%w: cannot retry from %s", ErrActionNotAllowed, w.State)
	}
	w.Attempts = 0
	w.LastError = ""
	w.BlockedReason = ""
	if err := s.setState(ctx, w, StateQueued, "人工重试，重新排队", "console"); err != nil {
		return nil, err
	}
	s.Wake()
	return w, nil
}

// StopWorker 取消当前 turn 并结束 worker。
func (s *Scheduler) StopWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := s.loadWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.State == StateClosed {
		return w, nil
	}
	s.closeWorker(ctx, w, "人工停止")
	fresh, _ := s.repo.GetWorker(ctx, id)
	if fresh == nil {
		fresh = w
	}
	return fresh, nil
}

// Act 执行控制台动作。
func (s *Scheduler) Act(ctx context.Context, id, action string) (*Worker, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "pause":
		return s.PauseWorker(ctx, id)
	case "resume":
		return s.ResumeWorker(ctx, id)
	case "retry":
		return s.RetryWorker(ctx, id)
	case "stop":
		return s.StopWorker(ctx, id)
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrActionNotAllowed, action)
	}
}

// execute 跑一轮完整修复：worktree → codex → 提交 → 推送 → PR。
func (s *Scheduler) execute(ctx context.Context, w *Worker, trigger string) error {
	p, err := s.resolver.ByID(ctx, w.ProjectID)
	if err != nil {
		return err
	}
	if s.workspace == nil || s.committer == nil || s.vcs == nil {
		return errors.New("scheduler: workspace/committer/vcs not configured")
	}
	if syncer, ok := s.tracker.(RepoSyncer); ok {
		// 每次开跑前同步一次：agents 改动必须基于当前 main，而不是克隆那天的代码。
		if err := syncer.SyncRepo(ctx, p.ID); err != nil {
			log.Printf("[scheduler] sync repo before issue %d failed, using existing refs: %v", w.Issue, err)
		}
	}
	dir, branch, err := s.workspace.Prepare(ctx, p, w.Issue, w.PRNumber > 0)
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

	w.Model = firstNonEmpty(p.Model, s.cfg.Model)
	w.ProviderID = p.ProviderID
	if err := s.setState(ctx, w, StateCoding, "Codex 开始修改代码", "scheduler"); err != nil {
		return err
	}
	res, err := s.runWorkerTurn(ctx, w, codexrunner.Spec{
		WorkDir:          dir,
		Env:              s.resolveModelEnv(ctx, p),
		Prompt:           buildFixPrompt(p, issue, comments, trigger, ""),
		CodexPath:        s.cfg.CodexPath,
		Model:            w.Model,
		Effort:           firstNonEmpty(p.Effort, s.cfg.Effort),
		BaseInstructions: FixSystemPrompt,
		Sandbox:          s.cfg.CodexSandbox,
		Timeout:          s.cfg.TurnTimeout,
		KeepResponse:     true,
	})
	if err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	patch, err := s.differ.Diff(ctx, dir, "")
	if err != nil {
		return fmt.Errorf("diff: %w", err)
	}
	if strings.TrimSpace(patch) == "" {
		w.LastError = ""
		if err := s.setState(ctx, w, StateAwaitingInput, "本轮没有代码改动，等待人工补充信息", "scheduler"); err != nil {
			return err
		}
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(
			"这一轮没有产生代码改动。需要补充信息或确认方案：\n\n"+clamp(res.FinalResponse, 3000)))
		s.notifyIssue(ctx, p, w, "需要补充信息："+clamp(res.FinalResponse, 120))
		return nil
	}
	if err := s.setState(ctx, w, StatePushing, "开始提交并推送", "scheduler"); err != nil {
		return err
	}
	commit, err := s.committer.Commit(ctx, dir, commitMessage(issue))
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	head, err := s.vcs.Push(ctx, p, w)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	if head != "" && !strings.HasPrefix(head, commit) {
		log.Printf("[scheduler] push head %s != commit %s (issue %d)", shortSHA(head), shortSHA(commit), w.Issue)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	base := firstNonEmpty(p.DefaultBase, "main")
	if w.PRNumber == 0 {
		pr, err := s.tracker.CreatePR(ctx, p.ID, w.Issue, branch, base,
			prTitle(issue), prBody(issue, commit))
		if err != nil {
			return fmt.Errorf("create pr: %w", err)
		}
		w.PRNumber = pr.Number
		w.PRURL = pr.URL
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(fmt.Sprintf(
			"已提交 `%s` 并创建 PR #%d。\n%s", shortSHA(commit), pr.Number, pr.URL)))
	} else {
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(fmt.Sprintf(
			"已追加提交 `%s` 到 PR #%d。", shortSHA(commit), w.PRNumber)))
	}
	w.LastError = ""
	// 提交完成后直接合并进基线分支：合并成功就收口，失败才把原因交给人工。
	merger, canMerge := s.tracker.(PullMerger)
	if !canMerge {
		s.notifyIssue(ctx, p, w, fmt.Sprintf("PR #%d 待合并：%s", w.PRNumber, w.PRURL))
		return s.setState(ctx, w, StateAwaitingReview, "PR 已就绪，等待 review", "scheduler")
	}
	if mergeErr := merger.MergePR(ctx, p.ID, w.Issue, w.PRNumber, prTitle(issue)); mergeErr != nil {
		_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(fmt.Sprintf(
			"PR #%d 自动合并失败：%s\n需要人工处理：%s", w.PRNumber, shortErr(mergeErr), w.PRURL)))
		s.notifyIssue(ctx, p, w, fmt.Sprintf("PR #%d 合并失败：%s\n%s", w.PRNumber, clamp(shortErr(mergeErr), 80), w.PRURL))
		return s.setState(ctx, w, StateAwaitingReview, "自动合并失败，等待人工处理", "scheduler")
	}
	_ = s.tracker.Comment(ctx, p.ID, w.Issue, botComment(fmt.Sprintf("PR #%d 已自动合并进 `%s`。", w.PRNumber, base)))
	s.notifyIssue(ctx, p, w, fmt.Sprintf("PR #%d 已合并进 %s：%s", w.PRNumber, base, w.PRURL))
	return s.setState(ctx, w, StateClosed, "PR 已合并进 "+base, "scheduler")
}

func commitMessage(issue Issue) string {
	return fmt.Sprintf("fix: %s (#%d)", clamp(strings.ReplaceAll(issue.Title, "\n", " "), 60), issue.Number)
}

func prTitle(issue Issue) string {
	return fmt.Sprintf("fix: %s (#%d)", clamp(strings.ReplaceAll(issue.Title, "\n", " "), 60), issue.Number)
}

func prBody(issue Issue, commit string) string {
	return fmt.Sprintf("## 变更说明\n\n自动修复 Issue #%d：%s\n\n- 提交：`%s`\n- 验证：Codex 自测（smartpipe.yaml）\n\nFixes #%d",
		issue.Number, issue.Title, shortSHA(commit), issue.Number)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func (s *Scheduler) failWorker(ctx context.Context, w *Worker, cause error) {
	if errors.Is(cause, context.Canceled) {
		return
	}
	log.Printf("[scheduler] worker %s (project=%s issue=%d) failed: %v", w.ID, w.ProjectID, w.Issue, cause)
	if ctx == nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}
	if fresh, err := s.repo.GetWorker(ctx, w.ID); err == nil && fresh != nil {
		w = fresh
	}
	if w.State == StatePaused || w.State == StateClosed || w.State == StateFailed {
		return
	}
	w.Attempts++
	w.LastError = cause.Error()
	state := StateBlocked
	w.BlockedReason = clamp(cause.Error(), 500)
	if w.Attempts < s.cfg.MaxAttempts {
		state = StateQueued
		w.BlockedReason = ""
	}
	if err := s.setState(ctx, w, state, "worker 执行失败："+clamp(cause.Error(), 300), "scheduler"); err != nil {
		log.Printf("[scheduler] update failed worker %s: %v", w.ID, err)
		return
	}
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
	if ctx == nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}
	_ = s.setState(ctx, w, StateClosed, "worker 已结束："+reason, "scheduler")
	done := s.cancelRun(w.ID)
	cleanup := func() {
		if s.workspace == nil {
			return
		}
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if p, err := s.resolver.ByID(bg, w.ProjectID); err == nil {
			_ = s.workspace.Cleanup(bg, p, w.Issue)
		}
	}
	if done != nil {
		go func() {
			select {
			case <-done:
			case <-time.After(30 * time.Second):
			}
			cleanup()
		}()
	} else {
		cleanup()
	}
	if w.ChatID != "" {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text := fmt.Sprintf("Issue #%d 已结束（%s）。", w.Issue, reason)
		if w.IssueURL != "" {
			text += "\n" + w.IssueURL
		}
		_ = s.notify(bg, "", w.ChatID, "", text)
	}
}

func (s *Scheduler) notify(ctx context.Context, botID, chatID, replyTo, text string) error {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	if s.notifier == nil {
		return nil
	}
	text = clamp(strings.TrimSpace(text), groupMessageLimit)
	if err := s.notifier.SendGroup(ctx, botID, chatID, replyTo, text); err != nil {
		return err
	}
	_ = s.repo.RecordChatReply(ctx, chatID, botID, s.now())
	return nil
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

// shortErr 把内部错误压成一条适合发到群里的短句，避免刷屏和泄露细节。
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
	return msg
}

const (
	maxChatAttachments     = 4
	maxChatAttachmentBytes = 8 << 20
)

// saveChatAttachments 把消息附件落到临时目录：图片作为图片输入交给模型，
// 其他文件给出本地路径让模型按需读取；调用方负责执行 cleanup。
func saveChatAttachments(msg Inbound) (images []string, listing []string, cleanup func(), err error) {
	cleanup = func() {}
	if len(msg.Attachments) == 0 {
		return nil, nil, cleanup, nil
	}
	dir, err := os.MkdirTemp("", "smartpipe-chat-attach-*")
	if err != nil {
		return nil, nil, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	saved := 0
	for i, a := range msg.Attachments {
		if saved >= maxChatAttachments {
			listing = append(listing, fmt.Sprintf("- 其余附件超过 %d 个上限，已忽略", maxChatAttachments))
			break
		}
		if len(a.Data) == 0 {
			continue
		}
		if len(a.Data) > maxChatAttachmentBytes {
			listing = append(listing, fmt.Sprintf("- %s（%d 字节，超过单文件上限，已忽略）", a.Name, len(a.Data)))
			continue
		}
		mimeType := strings.TrimSpace(a.MimeType)
		if mimeType == "" {
			mimeType = http.DetectContentType(a.Data)
		}
		name := chatAttachmentName(a.Name, i, mimeType)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, a.Data, 0o600); err != nil {
			return nil, nil, cleanup, err
		}
		saved++
		if strings.HasPrefix(mimeType, "image/") {
			images = append(images, path)
			listing = append(listing, fmt.Sprintf("- 图片 %s（%s）", name, mimeType))
		} else {
			listing = append(listing, fmt.Sprintf("- 文件 %s（%s）：%s", name, mimeType, path))
		}
	}
	return images, listing, cleanup, nil
}

// chatAttachmentName 只保留安全的文件名与扩展名，避免路径穿越和重名。
func chatAttachmentName(name string, index int, mimeType string) string {
	base := filepath.Base(strings.TrimSpace(name))
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		case r == ' ':
			return '-'
		default:
			return -1
		}
	}, base)
	if base == "" || base == "." || base == ".." {
		base = fmt.Sprintf("attachment-%d", index+1)
	}
	if filepath.Ext(base) == "" {
		if ext := extensionForMime(mimeType); ext != "" {
			base += ext
		}
	}
	return base
}

// mimeBase 去掉 mimeType 参数，例如 text/plain; charset=utf-8 -> text/plain。
func mimeBase(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

func extensionForMime(mimeType string) string {
	switch mimeBase(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	}
	return ""
}
