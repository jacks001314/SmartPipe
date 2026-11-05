package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testdb"
)

type testEnv struct {
	store    *store.Store
	gh       *httptest.Server
	creates  int32
	comments []string
	labels   []string
	bodies   []string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := testdb.DSN(t)
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i + 11)
	}
	box, _ := secretbox.New(key)
	st, err := store.Open(context.Background(), dsn, box)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = st.DB().Exec("DELETE FROM issue_approvals")
	_, _ = st.DB().Exec("DELETE FROM issue_plans")
	_, _ = st.DB().Exec("DELETE FROM bridge_dispatches")
	_, _ = st.DB().Exec("DELETE FROM bridge_links")
	_, _ = st.DB().Exec("DELETE FROM project_bindings")
	_, _ = st.DB().Exec("DELETE FROM webhook_deliveries")
	_, _ = st.DB().Exec("DELETE FROM user_identities")
	_, _ = st.DB().Exec("DELETE FROM credentials")
	_, _ = st.DB().Exec("DELETE FROM projects")
	t.Cleanup(func() {
		_, _ = st.DB().Exec("DELETE FROM issue_approvals")
		_, _ = st.DB().Exec("DELETE FROM issue_plans")
		_, _ = st.DB().Exec("DELETE FROM bridge_dispatches")
		_, _ = st.DB().Exec("DELETE FROM bridge_links")
		_, _ = st.DB().Exec("DELETE FROM project_bindings")
		_, _ = st.DB().Exec("DELETE FROM webhook_deliveries")
		_, _ = st.DB().Exec("DELETE FROM user_identities")
		_, _ = st.DB().Exec("DELETE FROM credentials")
		_, _ = st.DB().Exec("DELETE FROM projects")
		_ = st.Close()
	})

	env := &testEnv{store: st}
	env.gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if body, ok := payload["body"].(string); ok {
				env.comments = append(env.comments, body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{}"))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels") {
			var payload struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			env.labels = append(env.labels, payload.Labels...)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues") {
			atomic.AddInt32(&env.creates, 1)
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if body, ok := payload["body"].(string); ok {
				env.bodies = append(env.bodies, body)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"number":101,"html_url":"https://github.com/example/core/issues/101","title":%q}`, payload["title"])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(env.gh.Close)
	return env
}

func (e *testEnv) project(t *testing.T, enabled bool) *store.Project {
	t.Helper()
	p, err := e.store.CreateProject(context.Background(), &store.Project{
		Name:            fmt.Sprintf("bridge-%d", time.Now().UnixNano()),
		GitHubRepoURL:   "https://github.com/example/core",
		GitHubIssueRepo: "example/core",
		LabelsPrefix:    "smartpipe",
		Enabled:         enabled,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := e.store.CreateCredential(context.Background(), &p.ID, store.CredentialGitHubPAT, "gh-pat", "ghp_test_token_123456", map[string]any{"api_base": e.gh.URL}); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return p
}

func TestHandleMessageCreatesOnceAndDedupes(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	svc := NewService(env.store, nil)
	msg := Message{MessageID: "msg-1", ChatID: "grp-1", UserID: "u-1", Title: "启动崩溃", Body: "点击就崩", ProjectID: project.ID}

	link, created, err := svc.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !created || link.IssueNumber != 101 {
		t.Fatalf("unexpected link: %+v created=%v", link, created)
	}
	if got := atomic.LoadInt32(&env.creates); got != 1 {
		t.Fatalf("github calls = %d, want 1", got)
	}

	again, created, err := svc.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	if created {
		t.Fatal("duplicate message created a second issue")
	}
	if again.IssueURL != link.IssueURL {
		t.Fatalf("dedupe returned different issue: %s vs %s", again.IssueURL, link.IssueURL)
	}
	if got := atomic.LoadInt32(&env.creates); got != 1 {
		t.Fatalf("github calls after duplicate = %d, want 1", got)
	}

	links, err := env.store.ListBridgeLinks(context.Background(), 10)
	if err != nil || len(links) != 1 {
		t.Fatalf("links = %v err=%v", links, err)
	}
}

func TestHandleMessageRejectsDisabledProject(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, false)
	svc := NewService(env.store, nil)
	_, _, err := svc.HandleMessage(context.Background(), Message{MessageID: "msg-2", Title: "x", ProjectID: project.ID})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
	if got := atomic.LoadInt32(&env.creates); got != 0 {
		t.Fatalf("github should not be called, got %d", got)
	}
}

func TestHandleMessageRequiresProject(t *testing.T) {
	env := newEnv(t)
	svc := NewService(env.store, nil)
	_, _, err := svc.HandleMessage(context.Background(), Message{MessageID: "msg-3", Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("expected project_id error, got %v", err)
	}
}

func TestWebhookDedupesDelivery(t *testing.T) {
	env := newEnv(t)
	h := &WebhookHandler{Store: env.store, Secret: secret}
	body := []byte(`{"action":"opened","repository":{"full_name":"example/core"},"issue":{"number":5}}`)
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
		req.Header.Set("X-Hub-Signature-256", Sign(secret, body))
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-GitHub-Delivery", "delivery-dup")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(); code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", Sign(secret, body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-dup")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("second delivery = %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := env.store.CountWebhookDeliveries(context.Background()); n != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", n)
	}
}

type fakeDispatcher struct {
	agentID string
	err     error
	calls   int
	title   string
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, link *store.BridgeLink, project *store.Project, title string) (string, error) {
	f.calls++
	f.title = title
	return f.agentID, f.err
}

func TestDispatchToBoundLead(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	if _, err := env.store.UpsertBinding(context.Background(), store.ProjectBinding{
		ProjectID: project.ID, AppServerID: "appsrv-1", AppServerAddr: "http://127.0.0.1:1", LeadAgentID: "agent-1",
	}); err != nil {
		t.Fatalf("binding: %v", err)
	}
	fake := &fakeDispatcher{agentID: "agent-1"}
	svc := NewService(env.store, nil).WithDispatcher(fake)
	link, created, err := svc.HandleMessage(context.Background(), Message{MessageID: "msg-d1", Title: "登录失败", ProjectID: project.ID})
	if err != nil || !created {
		t.Fatalf("handle: err=%v created=%v", err, created)
	}
	if fake.calls != 1 || fake.title != "登录失败" {
		t.Fatalf("dispatcher calls=%d title=%q", fake.calls, fake.title)
	}
	if link.DispatchStatus != "sent" || link.LeadAgentID != "agent-1" || link.DispatchedAt == nil {
		t.Fatalf("link dispatch state: %+v", link)
	}
	items, err := env.store.ListDispatches(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].Status != "sent" || items[0].AgentID != "agent-1" {
		t.Fatalf("dispatches = %+v err=%v", items, err)
	}
}

func TestDispatchUnboundProject(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	svc := NewService(env.store, nil).WithDispatcher(NewLeadDispatcher(env.store))
	link, created, err := svc.HandleMessage(context.Background(), Message{MessageID: "msg-d2", Title: "闪退", ProjectID: project.ID})
	if err != nil || !created {
		t.Fatalf("handle: err=%v created=%v", err, created)
	}
	if link.DispatchStatus != "unbound" {
		t.Fatalf("dispatch status = %q, want unbound", link.DispatchStatus)
	}
}

func TestDispatchFailureKeepsLink(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	fake := &fakeDispatcher{err: errors.New("app server down")}
	svc := NewService(env.store, nil).WithDispatcher(fake)
	link, created, err := svc.HandleMessage(context.Background(), Message{MessageID: "msg-d3", Title: "白屏", ProjectID: project.ID})
	if err != nil || !created {
		t.Fatalf("handle: err=%v created=%v", err, created)
	}
	if link.DispatchStatus != "failed" || !strings.Contains(link.DispatchError, "app server down") {
		t.Fatalf("link = %+v", link)
	}
}

func TestLeadDispatcherSendsMessageToAppServer(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	var gotPath, gotProject, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotProject, _ = body["projectId"].(string)
		gotText, _ = body["text"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messageId":"m-1"}`))
	}))
	defer srv.Close()
	if _, err := env.store.UpsertBinding(context.Background(), store.ProjectBinding{
		ProjectID: project.ID, AppServerID: "appsrv-1", AppServerAddr: srv.URL, LeadAgentID: "agent-9",
	}); err != nil {
		t.Fatalf("binding: %v", err)
	}
	link, created, err := NewService(env.store, nil).WithDispatcher(NewLeadDispatcher(env.store)).
		HandleMessage(context.Background(), Message{MessageID: "msg-d4", ChatID: "grp", Title: "支付失败", ProjectID: project.ID})
	if err != nil || !created {
		t.Fatalf("handle: err=%v created=%v", err, created)
	}
	if gotPath != "/api/agents/agent-9/messages" || gotProject != project.ID {
		t.Fatalf("path=%q project=%q", gotPath, gotProject)
	}
	if !strings.Contains(gotText, link.IssueURL) || !strings.Contains(gotText, "/approve vN") {
		t.Fatalf("message text missing issue url or approval rule: %s", gotText)
	}
	if link.DispatchStatus != "sent" {
		t.Fatalf("dispatch status = %q", link.DispatchStatus)
	}
}

type fakeNotifier struct {
	calls  int
	target string
	text   string
	err    error
}

func (f *fakeNotifier) Reply(ctx context.Context, chatType, targetID, refMsgID, text string) error {
	f.calls++
	f.target = targetID
	f.text = text
	return f.err
}

func TestReceiptSentToSourceGroup(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	fake := &fakeNotifier{}
	svc := NewService(env.store, nil).WithNotifier(fake)
	link, created, err := svc.HandleMessage(context.Background(), Message{
		MessageID: "msg-r1", ChatID: "grp-9", Title: "回执验证", ProjectID: project.ID,
	})
	if err != nil || !created {
		t.Fatalf("handle: %v created=%v", err, created)
	}
	if fake.calls != 1 || fake.target != "grp-9" {
		t.Fatalf("notifier calls=%d target=%q", fake.calls, fake.target)
	}
	if !strings.Contains(fake.text, link.IssueURL) || !strings.Contains(fake.text, "#101") {
		t.Fatalf("receipt text missing issue: %s", fake.text)
	}
	if link.ReceiptStatus != "sent" {
		t.Fatalf("receipt status = %q", link.ReceiptStatus)
	}
}

func TestReceiptFailureKeepsIssue(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	fake := &fakeNotifier{err: errors.New("bot offline")}
	svc := NewService(env.store, nil).WithNotifier(fake)
	link, created, err := svc.HandleMessage(context.Background(), Message{
		MessageID: "msg-r2", ChatID: "grp-9", Title: "回执失败", ProjectID: project.ID,
	})
	if err != nil || !created {
		t.Fatalf("handle: %v created=%v", err, created)
	}
	if link.ReceiptStatus != "failed" || !strings.Contains(link.ReceiptError, "bot offline") {
		t.Fatalf("link = %+v", link)
	}
}

func TestBotNotifierHTTP(t *testing.T) {
	var gotPath, gotToken, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"ok\":true}"))
	}))
	defer srv.Close()
	if err := NewBotNotifier(srv.URL, "tok").Reply(context.Background(), "group", "grp-1", "msg-1", "已建单"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if gotPath != "/internal/reply" || gotToken != "tok" {
		t.Fatalf("path=%q token=%q", gotPath, gotToken)
	}
	if !strings.Contains(gotBody, "grp-1") || !strings.Contains(gotBody, "chatType") {
		t.Fatalf("body = %s", gotBody)
	}
}

func TestHandleMessageAddsReporterAttribution(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	if _, err := env.store.UpsertUserIdentity(context.Background(), store.UserIdentity{
		Platform: "lanxin", PlatformUserID: "u-42", GitHubLogin: "@alice",
		DisplayName: "爱丽丝", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert identity: %v", err)
	}
	svc := NewService(env.store, nil)
	msg := Message{MessageID: "msg-attr", ChatID: "grp-1", UserID: "u-42", Title: "登录崩溃", Body: "点登录就崩", ProjectID: project.ID}
	if _, _, err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(env.bodies) != 1 {
		t.Fatalf("issue bodies = %d", len(env.bodies))
	}
	body := env.bodies[0]
	if !strings.Contains(body, "@alice") || !strings.Contains(body, "爱丽丝") || !strings.Contains(body, "u-42") {
		t.Fatalf("attribution missing: %s", body)
	}
	if !strings.Contains(body, "点登录就崩") {
		t.Fatalf("original body lost: %s", body)
	}
}

func TestHandleMessageWithoutIdentityKeepsBody(t *testing.T) {
	env := newEnv(t)
	project := env.project(t, true)
	svc := NewService(env.store, nil)
	msg := Message{MessageID: "msg-plain", UserID: "u-unknown", Title: "崩溃", Body: "原始描述", ProjectID: project.ID}
	if _, _, err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(env.bodies) != 1 || env.bodies[0] != "原始描述" {
		t.Fatalf("body = %v", env.bodies)
	}
}
