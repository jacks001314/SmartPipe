package releaseagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"smartpipe/backend/internal/codexrunner"
	"smartpipe/backend/internal/releasev2"
)

type fakeRepo struct {
	mu       sync.Mutex
	tokens   []releasev2.Token
	sessions map[string]*releasev2.AgentSession
	events   []releasev2.Event
	revoked  []string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{sessions: map[string]*releasev2.AgentSession{}}
}

func (f *fakeRepo) CreateReleaseV2Token(_ context.Context, token releasev2.Token) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	return nil
}

func (f *fakeRepo) CreateAgentSession(_ context.Context, in *releasev2.AgentSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	in.CreatedAt = time.Now().UTC()
	copied := *in
	f.sessions[in.ID] = &copied
	return nil
}

func (f *fakeRepo) UpdateAgentSession(_ context.Context, in *releasev2.AgentSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *in
	f.sessions[in.ID] = &copied
	return nil
}

func (f *fakeRepo) RevokeReleaseV2Token(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	return nil
}

func (f *fakeRepo) AppendEvent(_ context.Context, ev releasev2.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeRepo) session(id string) *releasev2.AgentSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id]
}

func testBundle() *releasev2.RunBundle {
	return &releasev2.RunBundle{
		Run: &releasev2.Run{
			ID: "rrun_test", ProjectID: "prj_test", Version: "1.2.3", Mode: releasev2.ModePublish,
			Channel: "stable", Repo: "acme/widget", SourceBranch: "main", ResolvedCommit: "abc123",
			PlanSHA256: "sha256:deadbeef", Status: releasev2.RunQueued,
		},
		Targets: []releasev2.Target{{TargetID: "darwin-arm64", OS: "darwin", Arch: "arm64", ExecutorID: "mac-01", Status: releasev2.TargetQueued}},
	}
}

func TestLaunchRunsCodexWithScopedMCP(t *testing.T) {
	repo := newFakeRepo()
	var got codexrunner.Spec
	done := make(chan struct{})
	runner := RunnerFunc(func(_ context.Context, spec codexrunner.Spec) (*codexrunner.Result, error) {
		got = spec
		close(done)
		return &codexrunner.Result{Status: "completed", FinalResponse: "done", ThreadID: "thr", TurnID: "turn"}, nil
	})
	launcher := New(repo, runner, Config{
		Enabled: true, MCPURL: "http://127.0.0.1:18097/mcp", WorkRoot: t.TempDir(),
		Timeout: 5 * time.Second,
	})
	session, err := launcher.Launch(context.Background(), testBundle(), Options{Actor: "tester"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	<-done
	// 会话上下文已经结束，等 goroutine 落库。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := repo.session(session.ID); s != nil && !s.Status.Active() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	joined := strings.Join(got.ConfigOverrides, "\n")
	if !strings.Contains(joined, "mcp_servers.smartpipe_release.url=") {
		t.Fatalf("missing mcp url override: %s", joined)
	}
	if !strings.Contains(joined, "bearer_token_env_var=") {
		t.Fatalf("missing bearer env override: %s", joined)
	}
	tokenEnv := ""
	for _, kv := range got.Env {
		if strings.HasPrefix(kv, "SMARTPIPE_RELEASE_MCP_TOKEN=") {
			tokenEnv = kv
		}
	}
	if !strings.HasPrefix(tokenEnv, "SMARTPIPE_RELEASE_MCP_TOKEN=rmt_") {
		t.Fatalf("token env missing: %v", got.Env)
	}
	if strings.Contains(got.Prompt, "rmt_") {
		t.Fatal("prompt must not contain the raw token")
	}
	if !strings.Contains(got.Prompt, "rrun_test") || !strings.Contains(got.Prompt, "build_dispatch") {
		t.Fatalf("prompt is missing run id or tools: %s", got.Prompt)
	}
	if len(repo.tokens) != 1 || repo.tokens[0].RunID != "rrun_test" {
		t.Fatalf("token not stored for run: %+v", repo.tokens)
	}
	if len(repo.tokens[0].Scopes) != len(releasev2.ReleaseAgentScopes) {
		t.Fatalf("scopes mismatch: %v", repo.tokens[0].Scopes)
	}
	final := repo.session(session.ID)
	if final == nil || final.Status != releasev2.AgentSessionFinished {
		t.Fatalf("session should finish: %+v", final)
	}
	if len(repo.revoked) != 1 || repo.revoked[0] != final.TokenID {
		t.Fatalf("token should be revoked on finish: %v token=%s", repo.revoked, final.TokenID)
	}
	if final.ThreadID != "thr" || final.Summary == "" {
		t.Fatalf("session metadata missing: %+v", final)
	}
	found := false
	for _, ev := range repo.events {
		if ev.Kind == "agent.started" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing agent.started event: %+v", repo.events)
	}
}

func TestLaunchRejectsSecondSession(t *testing.T) {
	repo := newFakeRepo()
	block := make(chan struct{})
	started := make(chan struct{})
	runner := RunnerFunc(func(ctx context.Context, _ codexrunner.Spec) (*codexrunner.Result, error) {
		close(started)
		<-block
		return &codexrunner.Result{Status: "completed"}, nil
	})
	launcher := New(repo, runner, Config{Enabled: true, MCPURL: "http://x/mcp", WorkRoot: t.TempDir(), Timeout: 5 * time.Second})
	if _, err := launcher.Launch(context.Background(), testBundle(), Options{}); err != nil {
		t.Fatalf("first launch: %v", err)
	}
	<-started
	if _, err := launcher.Launch(context.Background(), testBundle(), Options{}); err == nil {
		t.Fatal("second launch should be rejected")
	}
	close(block)
}

func TestLaunchRequiresEnabled(t *testing.T) {
	launcher := New(newFakeRepo(), RunnerFunc(func(context.Context, codexrunner.Spec) (*codexrunner.Result, error) {
		t.Fatal("runner must not be called")
		return nil, nil
	}), Config{})
	if _, err := launcher.Launch(context.Background(), testBundle(), Options{}); err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}

func TestRedactRemovesTokens(t *testing.T) {
	in := `Authorization: Bearer abcdefghijklmnopqrstuvwx token=rmt_0123456789abcdef0123 key "api_key":"supersecretvalue123"`
	out := Redact(in)
	for _, bad := range []string{"rmt_0123456789abcdef0123", "supersecretvalue123", "abcdefghijklmnopqrstuvwx"} {
		if strings.Contains(out, bad) {
			t.Fatalf("token %q not redacted: %s", bad, out)
		}
	}
}

func TestPromptStopsAtApproval(t *testing.T) {
	p := BuildPrompt(testBundle(), "http://x/mcp", "smartpipe_release")
	for _, want := range []string{"approval", "release_approval_list", "darwin-arm64", "mac-01"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestPromptAddsGitHubStepsForPublish(t *testing.T) {
	bundle := testBundle()
	publish := BuildPrompt(bundle, "http://x/mcp", "smartpipe_release")
	for _, want := range []string{"github_draft_create", "github_asset_upload", "github_release_get"} {
		if !strings.Contains(publish, want) {
			t.Fatalf("publish prompt missing %q", want)
		}
	}
	bundle.Run.Mode = releasev2.ModeStage
	stage := BuildPrompt(bundle, "http://x/mcp", "smartpipe_release")
	if strings.Contains(stage, "github_asset_upload 把每个制品传上去") {
		t.Fatal("stage prompt should not instruct github upload as a required step")
	}
}

func TestLaunchEnforcesMaxSessions(t *testing.T) {
	repo := newFakeRepo()
	block := make(chan struct{})
	runner := RunnerFunc(func(ctx context.Context, _ codexrunner.Spec) (*codexrunner.Result, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return &codexrunner.Result{Status: "completed", FinalResponse: "done"}, nil
	})
	launcher := New(repo, runner, Config{
		Enabled: true, MCPURL: "http://127.0.0.1:18097/mcp", WorkRoot: t.TempDir(),
		Timeout: 5 * time.Second, MaxSessions: 1,
	})
	first := testBundle()
	first.Run.ID = "rrun_first"
	if _, err := launcher.Launch(context.Background(), first, Options{}); err != nil {
		t.Fatalf("first launch: %v", err)
	}

	second := testBundle()
	second.Run.ID = "rrun_second"
	if _, err := launcher.Launch(context.Background(), second, Options{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("second launch err = %v, want ErrLimit", err)
	}
	if len(repo.tokens) != 1 {
		t.Fatalf("limited launch must not mint tokens: %d", len(repo.tokens))
	}

	close(block)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && launcher.Running("rrun_first") {
		time.Sleep(10 * time.Millisecond)
	}
	if launcher.Running("rrun_first") {
		t.Fatal("first session should have finished")
	}
	if _, err := launcher.Launch(context.Background(), second, Options{}); err != nil {
		t.Fatalf("launch after slot freed: %v", err)
	}
}