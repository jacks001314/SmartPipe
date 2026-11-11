package scheduler

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"smartpipe/backend/internal/codexrunner"
)

type fakeRunner struct {
	mu        sync.Mutex
	specs     []codexrunner.Spec
	chatReply string
	fixReply  string
	chatErr   error
	fixErr    error
}

func (f *fakeRunner) Run(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if spec.Sandbox == "read-only" {
		if f.chatErr != nil {
			return nil, f.chatErr
		}
		return &codexrunner.Result{Status: "completed", FinalResponse: f.chatReply}, nil
	}
	if f.fixErr != nil {
		return nil, f.fixErr
	}
	return &codexrunner.Result{Status: "completed", FinalResponse: f.fixReply}, nil
}

func (f *fakeRunner) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.specs {
		if kind == "chat" && s.Sandbox == "read-only" {
			n++
		}
		if kind == "fix" && s.Sandbox != "read-only" {
			n++
		}
	}
	return n
}

func (f *fakeRunner) last() codexrunner.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		return codexrunner.Spec{}
	}
	return f.specs[len(f.specs)-1]
}

type fakeTracker struct {
	mu       sync.Mutex
	states   map[int]string
	issues   []Issue
	comments map[int][]Comment
	searches []Issue
	prs      []PullRequest
	created  []string
	prCalls  int
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{comments: map[int][]Comment{}}
}

func (f *fakeTracker) CreateIssue(ctx context.Context, projectID, title, body string, labels []string) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := Issue{Number: len(f.issues) + 1, Title: title, Body: body, State: "open",
		URL: "https://github.com/acme/core/issues/" + itoa(len(f.issues)+1), Labels: labels}
	f.issues = append(f.issues, issue)
	f.created = append(f.created, title)
	return issue, nil
}

func (f *fakeTracker) GetIssue(ctx context.Context, projectID string, number int) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.issues {
		if it.Number == number {
			if st, ok := f.states[number]; ok {
				it.State = st
			}
			return it, nil
		}
	}
	state := "open"
	if st, ok := f.states[number]; ok {
		state = st
	}
	return Issue{Number: number, Title: "issue", State: state}, nil
}

func (f *fakeTracker) setState(number int, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[int]string{}
	}
	f.states[number] = state
}

func (f *fakeTracker) Comment(ctx context.Context, projectID string, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments[number] = append(f.comments[number], Comment{Body: body, Author: "smartpipe[bot]"})
	return nil
}

func (f *fakeTracker) ListComments(ctx context.Context, projectID string, number int) ([]Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Comment(nil), f.comments[number]...), nil
}

func (f *fakeTracker) SearchIssues(ctx context.Context, projectID, query string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Issue(nil), f.searches...), nil
}

func (f *fakeTracker) CreatePR(ctx context.Context, projectID string, issue int, head, base, title, body string) (PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prCalls++
	pr := PullRequest{Number: f.prCalls, URL: "https://github.com/acme/core/pull/1", State: "open"}
	f.prs = append(f.prs, pr)
	return pr, nil
}

func (f *fakeTracker) issueCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.issues)
}

func (f *fakeTracker) issueAt(i int) Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issues[i]
}

func (f *fakeTracker) prCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prCalls
}

func (f *fakeTracker) commentsFor(n int) []Comment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Comment(nil), f.comments[n]...)
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeNotifier) SendGroup(ctx context.Context, botID, chatID, replyTo, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeNotifier) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

type fakeWorkspace struct {
	mu        sync.Mutex
	prepared  int
	cleaned   int
	lastIssue int
}

func (f *fakeWorkspace) Prepare(ctx context.Context, p *Project, issue int, preserve bool) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	f.lastIssue = issue
	return "/tmp/ws/" + p.ID + "/issue-" + itoa(issue), "agent/issue-" + itoa(issue), nil
}

func (f *fakeWorkspace) Cleanup(ctx context.Context, p *Project, issue int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleaned++
	return nil
}

func (f *fakeWorkspace) cleanedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cleaned
}

func (f *fakeWorkspace) issueSeen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastIssue
}

type fakeCommitter struct {
	mu      sync.Mutex
	commits int
}

func (f *fakeCommitter) Commit(ctx context.Context, dir, message string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	return "abcdef1234567890", nil
}

type fakeVCS struct {
	mu     sync.Mutex
	pushes int
}

func (f *fakeVCS) Push(ctx context.Context, p *Project, w *Worker) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes++
	return "abcdef1234567890", nil
}

type fixedResolver struct{ project *Project }

func (r fixedResolver) ByChat(ctx context.Context, chatID string) (*Project, error) {
	if chatID == "group-1" {
		return r.project, nil
	}
	return nil, ErrUnbound
}

func (r fixedResolver) ByID(ctx context.Context, projectID string) (*Project, error) {
	if projectID == r.project.ID {
		return r.project, nil
	}
	return nil, ErrUnbound
}

type harness struct {
	s         *Scheduler
	repo      *MemoryRepo
	runner    *fakeRunner
	tracker   *fakeTracker
	notifier  *fakeNotifier
	ws        *fakeWorkspace
	committer *fakeCommitter
	vcs       *fakeVCS
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{
		repo:      NewMemoryRepo(),
		runner:    &fakeRunner{},
		tracker:   newFakeTracker(),
		notifier:  &fakeNotifier{},
		ws:        &fakeWorkspace{},
		committer: &fakeCommitter{},
		vcs:       &fakeVCS{},
	}
	project := &Project{ID: "prj-1", Name: "core", Repo: "acme/core", DefaultBase: "main", LocalPath: "/tmp/repo"}
	s, err := New(cfg, Deps{
		Repo: h.repo, Runner: h.runner, Differ: DifferFunc(func(ctx context.Context, dir, git string, ex ...string) (string, error) {
			return "diff --git a/x b/x\n+fix", nil
		}),
		Tracker: h.tracker, Notifier: h.notifier, Resolver: fixedResolver{project: project},
		Workspace: h.ws, Committer: h.committer, VCS: h.vcs,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	h.s = s
	return h
}

func itoa(n int) string { return strconv.Itoa(n) }

func msg(id, text string) Inbound {
	return Inbound{Platform: "lanxin", PlatformMessageID: id, ChatType: "group", GroupID: "group-1",
		GroupName: "研发群", SenderID: "u1", SenderName: "张三", Text: text, BotID: "bot-1", IsAtMe: true}
}

func TestChatAnswerRepliesToGroup(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"这个功能在设置页开启。"}`
	if err := h.s.HandleMessage(context.Background(), msg("m1", "怎么开启自动同步？")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.tracker.issueCount() != 0 {
		t.Fatalf("should not create issue, got %d", len(h.tracker.issues))
	}
	sent := h.notifier.all()
	if len(sent) != 1 || !strings.Contains(sent[0], "设置页") {
		t.Fatalf("reply not sent: %#v", sent)
	}
	if h.runner.last().BaseInstructions != ChatSystemPrompt {
		t.Fatal("chat worker must use ChatSystemPrompt")
	}
	if h.runner.last().Sandbox != "read-only" {
		t.Fatalf("chat worker must be read-only, got %q", h.runner.last().Sandbox)
	}
}

func TestChatCreatesIssueAndFixWorkerProducesPR(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"issue","title":"导出按钮点击无响应","body":"点导出没反应","severity":"high"}`
	if err := h.s.HandleMessage(context.Background(), msg("m2", "导出点了没反应")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.tracker.issueCount() != 0 {
		t.Fatalf("issue must wait for group confirmation, got %d", h.tracker.issueCount())
	}
	if len(h.notifier.all()) != 1 || !strings.Contains(h.notifier.all()[0], "待确认建单") {
		t.Fatalf("group should receive draft confirmation: %#v", h.notifier.all())
	}
	draftID := issueDraftIDPattern.FindString(h.notifier.all()[0])
	if draftID == "" {
		t.Fatalf("draft id missing: %#v", h.notifier.all())
	}
	if err := h.s.HandleMessage(context.Background(), msg("m2-confirm", "确认建单 "+draftID)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if h.tracker.issueCount() != 1 {
		t.Fatalf("issue not created after confirmation: %d", len(h.tracker.issues))
	}
	if !strings.Contains(h.notifier.all()[len(h.notifier.all())-1], "Issue #1") {
		t.Fatalf("group should receive issue link: %#v", h.notifier.all())
	}
	w, err := h.repo.ActiveWorker(context.Background(), "prj-1", 1)
	if err != nil || w == nil || w.State != StateQueued {
		t.Fatalf("worker not queued: %+v err=%v", w, err)
	}

	h.runner.fixReply = "已修复导出按钮的点击事件。"
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	h.s.Wait()

	w, _ = h.repo.ActiveWorker(context.Background(), "prj-1", 1)
	if w.State != StateAwaitingReview {
		t.Fatalf("want awaiting_review, got %s (%s)", w.State, w.LastError)
	}
	if h.tracker.prCount() != 1 || h.vcs.pushes != 1 {
		t.Fatalf("pr=%d pushes=%d", h.tracker.prCount(), h.vcs.pushes)
	}
	if h.runner.last().BaseInstructions != FixSystemPrompt {
		t.Fatal("fix worker must use FixSystemPrompt")
	}
	if h.ws.issueSeen() != 1 {
		t.Fatalf("worktree issue=%d", h.ws.issueSeen())
	}
	if len(h.tracker.commentsFor(1)) == 0 {
		t.Fatal("issue should get a comment")
	}
	found := false
	for _, s := range h.notifier.all() {
		if strings.Contains(s, "PR #1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("group should be notified about PR: %#v", h.notifier.all())
	}
}

func TestMessageIdempotent(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"ok"}`
	for i := 0; i < 3; i++ {
		if err := h.s.HandleMessage(context.Background(), msg("same-id", "重复消息")); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	if h.runner.count("chat") != 1 {
		t.Fatalf("chat worker ran %d times", h.runner.count("chat"))
	}
	if len(h.notifier.all()) != 1 {
		t.Fatalf("reply sent %d times", len(h.notifier.all()))
	}
}

func TestDuplicateIssueReusesExisting(t *testing.T) {
	h := newHarness(t, Config{})
	h.tracker.searches = []Issue{{Number: 7, Title: "导出按钮点击无响应", State: "open", URL: "https://x/7"}}
	h.runner.chatReply = `{"action":"issue","title":"导出按钮点击无响应","body":"又出现了","severity":"high"}`
	if err := h.s.HandleMessage(context.Background(), msg("m3", "导出又没反应")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if h.tracker.issueCount() != 0 {
		t.Fatalf("should not create duplicate issue")
	}
	if len(h.tracker.commentsFor(7)) != 1 {
		t.Fatalf("should comment on existing issue")
	}
	if !strings.Contains(h.notifier.all()[0], "Issue #7") {
		t.Fatalf("reply: %#v", h.notifier.all())
	}
}

func TestIssueCommentResumesSameWorker(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 5, State: StateAwaitingReview, ChatID: "group-1", PRNumber: 9, Branch: "agent/issue-5"}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	err := h.s.HandleIssueEvent(context.Background(), IssueEvent{
		Event: "issue_comment", Action: "created", ProjectID: "prj-1", Repo: "acme/core",
		Issue: 5, Author: "lisi", Body: "这里还要兼容旧版本，请调整一下",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	if h.runner.count("fix") != 1 {
		t.Fatalf("fix runs=%d", h.runner.count("fix"))
	}
	spec := h.runner.last()
	if !strings.Contains(spec.Prompt, "兼容旧版本") {
		t.Fatalf("trigger not injected: %s", spec.Prompt)
	}
	if h.tracker.prCount() != 0 {
		t.Fatal("should reuse existing PR")
	}
	w, _ = h.repo.ActiveWorker(context.Background(), "prj-1", 5)
	if w.State != StateAwaitingReview {
		t.Fatalf("state=%s", w.State)
	}
}

func TestIssueClosedStopsWorker(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 6, State: StateAwaitingReview, ChatID: "group-1"}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	if err := h.s.HandleIssueEvent(context.Background(), IssueEvent{
		Event: "issues", Action: "closed", ProjectID: "prj-1", Repo: "acme/core", Issue: 6,
	}); err != nil {
		t.Fatal(err)
	}
	w, _ = h.repo.GetWorker(context.Background(), w.ID)
	if w.State != StateClosed {
		t.Fatalf("want closed, got %s", w.State)
	}
	if h.ws.cleanedCount() != 1 {
		t.Fatalf("workspace not cleaned: %d", h.ws.cleanedCount())
	}
}

func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t, Config{MaxGlobal: 1, MaxPerProject: 1})
	for i := 1; i <= 3; i++ {
		_, _ = h.repo.CreateWorker(context.Background(), &Worker{ProjectID: "prj-1", Issue: i, State: StateQueued, ChatID: "group-1"})
	}
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	if h.runner.count("fix") != 1 {
		t.Fatalf("concurrency limit violated: %d runs", h.runner.count("fix"))
	}
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	if h.runner.count("fix") != 2 {
		t.Fatalf("second dispatch should run next job, got %d", h.runner.count("fix"))
	}
}

func TestBotCommentsIgnored(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 8, State: StateAwaitingReview, ChatID: "group-1"}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	_ = h.s.HandleIssueEvent(context.Background(), IssueEvent{
		Event: "issue_comment", Action: "created", ProjectID: "prj-1", Issue: 8,
		Author: "smartpipe[bot]", Body: botComment("已提交到分支"),
	})
	h.s.Wait()
	if h.runner.count("fix") != 0 {
		t.Fatal("bot comment must not trigger a turn")
	}
}

func TestChatDecisionParsing(t *testing.T) {
	cases := []struct {
		raw    string
		action string
		ok     bool
	}{
		{`{"action":"answer","reply":"hi"}`, "answer", true},
		{"```json\n{\"action\":\"issue\",\"title\":\"bug\"}\n```", "issue", true},
		{`{"action":"issue_update","issue":12,"reply":"补充"}`, "issue_update", true},
		{`{"action":"issue"}`, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		d, err := ParseChatDecision(c.raw)
		if c.ok && (err != nil || d.Action != c.action) {
			t.Fatalf("%s: got %+v err=%v", c.raw, d, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s: want error", c.raw)
		}
	}
}

func TestPromptsAreRoleSpecific(t *testing.T) {
	if ChatSystemPrompt == FixSystemPrompt {
		t.Fatal("prompts must differ")
	}
	for _, want := range []string{"只读", "JSON", "Issue"} {
		if !strings.Contains(ChatSystemPrompt, want) {
			t.Fatalf("chat prompt missing %q", want)
		}
	}
	for _, want := range []string{"worktree", "commit", "smartpipe.yaml"} {
		if !strings.Contains(FixSystemPrompt, want) {
			t.Fatalf("fix prompt missing %q", want)
		}
	}
}

func TestUnboundChatRepliesAndFails(t *testing.T) {
	h := newHarness(t, Config{})
	err := h.s.HandleMessage(context.Background(), Inbound{PlatformMessageID: "m9", GroupID: "other", Text: "你好"})
	if !errors.Is(err, ErrUnbound) {
		t.Fatalf("want ErrUnbound, got %v", err)
	}
	if len(h.notifier.all()) != 1 || !strings.Contains(h.notifier.all()[0], "绑定项目") {
		t.Fatalf("reply: %#v", h.notifier.all())
	}
}

func TestChatFailureReplies(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatErr = errors.New("model down")
	err := h.s.HandleMessage(context.Background(), msg("m10", "在吗"))
	if err == nil {
		t.Fatal("want error")
	}
	if len(h.notifier.all()) != 1 {
		t.Fatalf("should tell user: %#v", h.notifier.all())
	}
}

func TestMemoryRepoUniqueActiveWorker(t *testing.T) {
	r := NewMemoryRepo()
	ctx := context.Background()
	a := &Worker{ProjectID: "p", Issue: 1, State: StateQueued}
	if ok, _ := r.CreateWorker(ctx, a); !ok {
		t.Fatal("first create failed")
	}
	b := &Worker{ProjectID: "p", Issue: 1, State: StateQueued}
	if ok, _ := r.CreateWorker(ctx, b); ok {
		t.Fatal("duplicate active worker allowed")
	}
	a.State = StateClosed
	_ = r.UpdateWorker(ctx, a)
	c := &Worker{ProjectID: "p", Issue: 1, State: StateQueued}
	if ok, _ := r.CreateWorker(ctx, c); !ok {
		t.Fatal("closed worker should allow recreation")
	}
}

func TestCommandStatusAndRetry(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 11, State: StateBlocked, ChatID: "group-1", LastError: "verify failed"}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	_ = h.s.HandleIssueEvent(context.Background(), IssueEvent{
		Event: "issue_comment", Action: "created", ProjectID: "prj-1", Issue: 11, Author: "wang", Body: "/status",
	})
	if len(h.tracker.commentsFor(11)) == 0 || !strings.Contains(h.tracker.commentsFor(11)[0].Body, "blocked") {
		t.Fatalf("status comment missing: %#v", h.tracker.commentsFor(11))
	}
	_ = h.s.HandleIssueEvent(context.Background(), IssueEvent{
		Event: "issue_comment", Action: "created", ProjectID: "prj-1", Issue: 11, Author: "wang", Body: "/retry",
	})
	_ = h.s.DispatchOnce(context.Background())
	h.s.Wait()
	w, _ = h.repo.ActiveWorker(context.Background(), "prj-1", 11)
	if w.State != StateAwaitingReview {
		t.Fatalf("retry should run the worker, state=%s", w.State)
	}
}

func TestWorkerFailureRetriesThenBlocks(t *testing.T) {
	h := newHarness(t, Config{MaxAttempts: 2})
	h.runner.fixErr = errors.New("codex crashed")
	w := &Worker{ProjectID: "prj-1", Issue: 12, State: StateQueued, ChatID: "group-1"}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	for i := 0; i < 3; i++ {
		_ = h.s.DispatchOnce(context.Background())
		h.s.Wait()
		time.Sleep(5 * time.Millisecond)
	}
	w, _ = h.repo.GetWorker(context.Background(), w.ID)
	if w.State != StateBlocked {
		t.Fatalf("want blocked after retries, got %s", w.State)
	}
	if w.Attempts < 2 {
		t.Fatalf("attempts=%d", w.Attempts)
	}
}

func TestHumanIssueCommentMirroredToGroup(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 21, State: StateAwaitingReview, ChatID: "group-1"}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	_ = h.s.HandleIssueEvent(context.Background(), IssueEvent{
		Event: "issue_comment", Action: "created", ProjectID: "prj-1", Repo: "acme/core",
		Issue: 21, Author: "lisi", Body: "这个方案可以，但请补一条测试",
	})
	h.s.Wait()
	var found bool
	for _, s := range h.notifier.all() {
		if strings.Contains(s, "lisi") && strings.Contains(s, "补一条测试") {
			found = true
		}
	}
	if !found {
		t.Fatalf("issue comment not mirrored to group: %#v", h.notifier.all())
	}
}

func TestReconcileClosesWorkerWhenIssueClosed(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 22, State: StateAwaitingReview, ChatID: "group-1", PRNumber: 7}
	_, _ = h.repo.CreateWorker(context.Background(), w)
	h.tracker.setState(22, "closed")
	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, _ = h.repo.GetWorker(context.Background(), w.ID)
	if w.State != StateClosed {
		t.Fatalf("reconcile should close worker, got %s", w.State)
	}
	if h.ws.cleanedCount() != 1 {
		t.Fatalf("worktree not cleaned")
	}
}

func TestReferencedIssueParsing(t *testing.T) {
	cases := map[string]int{
		"看下 #42 这个问题":                                 42,
		"https://github.com/acme/core/issues/77 还是复现": 77,
		"#12，再加一条":                                    12,
		"没有引用":                                        0,
	}
	for text, want := range cases {
		if got := referencedIssue(text); got != want {
			t.Fatalf("%q: got %d want %d", text, got, want)
		}
	}
}

func TestChatIssueUpdateEnqueuesWorker(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"issue_update","issue":33,"reply":"补充一个复现步骤"}`
	if err := h.s.HandleMessage(context.Background(), msg("m-update", "#33 还会在某些机器上闪退")); err != nil {
		t.Fatal(err)
	}
	if len(h.tracker.commentsFor(33)) != 1 {
		t.Fatalf("comment not posted: %#v", h.tracker.commentsFor(33))
	}
	w, _ := h.repo.ActiveWorker(context.Background(), "prj-1", 33)
	if w == nil || w.State != StateQueued {
		t.Fatalf("worker not enqueued: %+v", w)
	}
}

type blockingRunner struct {
	started  chan struct{}
	finished chan struct{}
}

func (b *blockingRunner) Run(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case b.finished <- struct{}{}:
	default:
	}
	return nil, ctx.Err()
}

func TestPauseWorkerCancelsActiveTurn(t *testing.T) {
	h := newHarness(t, Config{})
	br := &blockingRunner{started: make(chan struct{}, 1), finished: make(chan struct{}, 1)}
	h.s.runner = br
	w := &Worker{ProjectID: "prj-1", Issue: 31, State: StateQueued, ChatID: "group-1"}
	if _, err := h.repo.CreateWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-br.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	got, err := h.s.PauseWorker(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got.State != StatePaused {
		t.Fatalf("want paused, got %s", got.State)
	}
	select {
	case <-br.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("running turn was not cancelled")
	}
	h.s.Wait()
	fresh, _ := h.repo.GetWorker(context.Background(), w.ID)
	if fresh.State != StatePaused {
		t.Fatalf("pause must survive turn cancellation, got %s (%s)", fresh.State, fresh.LastError)
	}
	if h.tracker.prCount() != 0 || h.vcs.pushes != 0 {
		t.Fatal("paused worker must not push or create a PR")
	}
}

func TestStopWorkerCancelsAndCloses(t *testing.T) {
	h := newHarness(t, Config{})
	br := &blockingRunner{started: make(chan struct{}, 1), finished: make(chan struct{}, 1)}
	h.s.runner = br
	w := &Worker{ProjectID: "prj-1", Issue: 32, State: StateQueued, ChatID: "group-1"}
	if _, err := h.repo.CreateWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-br.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	got, err := h.s.StopWorker(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got.State != StateClosed {
		t.Fatalf("want closed, got %s", got.State)
	}
	select {
	case <-br.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("running turn was not cancelled")
	}
	h.s.Wait()
	fresh, _ := h.repo.GetWorker(context.Background(), w.ID)
	if fresh.State != StateClosed {
		t.Fatalf("stop must stay closed, got %s", fresh.State)
	}
	if h.tracker.prCount() != 0 || h.vcs.pushes != 0 {
		t.Fatal("stopped worker must not push or create a PR")
	}
}

func TestResumePausedWorkerRequeues(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 33, State: StatePaused, ChatID: "group-1", BlockedReason: "paused by human"}
	if _, err := h.repo.CreateWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.ResumeWorker(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.State != StateQueued {
		t.Fatalf("want queued, got %s", got.State)
	}
	h.runner.fixReply = "resumed"
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	fresh, _ := h.repo.GetWorker(context.Background(), w.ID)
	if fresh.State != StateAwaitingReview {
		t.Fatalf("want awaiting_review, got %s (%s)", fresh.State, fresh.LastError)
	}
}

func TestWorkerDetailIncludesTimeline(t *testing.T) {
	h := newHarness(t, Config{})
	w := &Worker{ProjectID: "prj-1", Issue: 34, State: StateQueued, ChatID: "group-1"}
	if _, err := h.repo.CreateWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	detail, err := h.s.WorkerDetail(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Worker == nil || detail.Worker.ProjectName != "core" {
		t.Fatalf("worker metadata missing: %+v", detail.Worker)
	}
	if len(detail.Events) == 0 {
		t.Fatal("timeline should contain state events")
	}
	if len(detail.Worker.AllowedActions) == 0 {
		t.Fatal("allowed actions should be computed")
	}
}

func TestRecoverMarksOrphanedActiveWorkers(t *testing.T) {
	h := newHarness(t, Config{})
	old := time.Now().Add(-5 * time.Minute)
	w := &Worker{ProjectID: "prj-1", Issue: 35, State: StateCoding, ChatID: "group-1"}
	if _, err := h.repo.CreateWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	w.HeartbeatAt = &old
	_ = h.repo.UpdateWorker(context.Background(), w)
	if err := h.s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh, _ := h.repo.GetWorker(context.Background(), w.ID)
	if fresh.State != StateBlocked {
		t.Fatalf("orphan should become blocked, got %s", fresh.State)
	}
	if !strings.Contains(fresh.BlockedReason, "重启") {
		t.Fatalf("reason missing: %q", fresh.BlockedReason)
	}
}

func TestMessageLifecycleRecorded(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"issue","title":"消息记录测试","body":"测试","severity":"low"}`
	if err := h.s.HandleMessage(context.Background(), msg("msg-record-1", "记录这条消息")); err != nil {
		t.Fatal(err)
	}
	draftID := issueDraftIDPattern.FindString(strings.Join(h.notifier.all(), "\n"))
	if draftID == "" {
		t.Fatalf("draft id missing: %#v", h.notifier.all())
	}
	if err := h.s.HandleMessage(context.Background(), msg("msg-record-confirm", "确认建单 "+draftID)); err != nil {
		t.Fatal(err)
	}
	list, err := h.repo.ListMessages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var original, confirmed *MessageRecord
	for i := range list {
		switch list[i].PlatformMessageID {
		case "msg-record-1":
			original = &list[i]
		case "msg-record-confirm":
			confirmed = &list[i]
		}
	}
	if original == nil || original.Status != "awaiting_confirmation" {
		t.Fatalf("original message should await confirmation: %+v", original)
	}
	if confirmed == nil || confirmed.Status != "issue_created" || confirmed.Issue != 1 {
		t.Fatalf("confirmation message should record issue creation: %+v", confirmed)
	}
}

func TestUnboundMessageRecorded(t *testing.T) {
	h := newHarness(t, Config{})
	err := h.s.HandleMessage(context.Background(), Inbound{PlatformMessageID: "msg-unbound", GroupID: "other", Text: "你好"})
	if !errors.Is(err, ErrUnbound) {
		t.Fatalf("want unbound, got %v", err)
	}
	list, _ := h.repo.ListMessages(context.Background(), 10)
	if len(list) != 1 || list[0].Status != "unbound" {
		t.Fatalf("unexpected unbound record: %+v", list)
	}
}
