// scheduler 是 SmartPipe 的调度器：把蓝信消息变成 Issue，把 Issue 变成可合并的 PR。
//
// 用户只和蓝信群交互；每个 Issue 绑定一个 fix worker，worker 直到 Issue 关闭才退出。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/codexrunner"
	"smartpipe/backend/internal/scheduler"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

type storeResolver struct{ st *store.Store }

func toProject(p *store.Project) *scheduler.Project {
	return &scheduler.Project{
		ID: p.ID, Name: p.Name,
		Repo:        firstNonEmpty(p.GitHubRepoURL, p.GitHubIssueRepo),
		IssueRepo:   p.GitHubIssueRepo,
		DefaultBase: firstNonEmpty(p.GitHubDefaultBranch, "main"),
		LocalPath:   p.LocalRepoPath,
		Model:       p.CodexModel,
		Effort:      p.CodexReasoningEffort,
		ProviderID:  p.CodexModelProvider,
	}
}

func (r storeResolver) ByID(ctx context.Context, projectID string) (*scheduler.Project, error) {
	p, err := r.st.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return toProject(p), nil
}

func (r storeResolver) ByChat(ctx context.Context, chatID string) (*scheduler.Project, error) {
	projects, err := r.st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		p := &projects[i]
		if !p.Enabled {
			continue
		}
		for _, g := range strings.Split(p.BluexGroupIDs, ",") {
			if strings.TrimSpace(g) == chatID && chatID != "" {
				return toProject(p), nil
			}
		}
	}
	return nil, scheduler.ErrUnbound
}

// modelEnvFetcher 从配置台读取供应商运行参数，注入 Codex 进程环境。
func modelEnvFetcher(adminURL, token string) func(ctx context.Context, projectID, providerID string) ([]string, error) {
	return func(ctx context.Context, projectID, providerID string) ([]string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimRight(adminURL, "/")+"/admin/model-providers/"+url.PathEscape(providerID)+"/runtime", nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("provider runtime http %d", resp.StatusCode)
		}
		var out struct {
			Data struct {
				BaseURL string `json:"base_url"`
				WireAPI string `json:"wire_api"`
				APIKey  string `json:"api_key"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		var env []string
		if out.Data.BaseURL != "" {
			env = append(env, "OPENAI_BASE_URL="+out.Data.BaseURL)
		}
		if out.Data.APIKey != "" {
			env = append(env, "OPENAI_API_KEY="+out.Data.APIKey)
		}
		if out.Data.WireAPI != "" {
			env = append(env, "OPENAI_WIRE_API="+out.Data.WireAPI)
		}
		return env, nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && n > 0 {
		return n
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := strings.TrimSpace(os.Getenv("SMARTPIPE_DSN"))
	box, err := secretbox.NewFromBase64(os.Getenv("SMARTPIPE_MASTER_KEY"))
	if err != nil {
		log.Fatalf("[scheduler] master key: %v", err)
	}
	st, err := store.Open(ctx, dsn, box)
	if err != nil {
		log.Fatalf("[scheduler] open store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("[scheduler] migrate: %v", err)
	}

	adminURL := firstNonEmpty(os.Getenv("SMARTPIPE_ADMIN_URL"), "http://127.0.0.1:8092")
	adminToken := os.Getenv("SMARTPIPE_ADMIN_TOKEN")
	botURL := firstNonEmpty(os.Getenv("BOT_ADDR"), "http://127.0.0.1:8093")
	internalToken := os.Getenv("INTERNAL_TOKEN")
	workspaceRoot := firstNonEmpty(os.Getenv("SCHEDULER_WORKSPACE_ROOT"), "/data/workspaces")
	repoCache := firstNonEmpty(os.Getenv("SCHEDULER_REPO_CACHE"), "/data/scheduler-repos")

	cfg := scheduler.Config{
		MaxGlobal:      envInt("SCHEDULER_MAX_GLOBAL", 3),
		MaxPerProject:  envInt("SCHEDULER_MAX_PER_PROJECT", 2),
		MaxAttempts:    envInt("SCHEDULER_MAX_ATTEMPTS", 3),
		TurnTimeout:    time.Duration(envInt("SCHEDULER_TURN_TIMEOUT_MIN", 30)) * time.Minute,
		PollInterval:   2 * time.Second,
		Model:          os.Getenv("SMARTPIPE_MODEL"),
		Effort:         os.Getenv("SMARTPIPE_MODEL_EFFORT"),
		CodexPath:      os.Getenv("CODEX_PATH"),
	}
	s, err := scheduler.New(cfg, scheduler.Deps{
		Repo:      scheduler.NewPgRepo(st.DB()),
		Runner:    scheduler.RunnerFunc(codexrunner.Run),
		Differ:    scheduler.DifferFunc(codexrunner.Diff),
		Tracker:   scheduler.NewHTTPTracker(adminURL, adminToken),
		Notifier:  scheduler.NewHTTPNotifier(botURL, internalToken),
		Resolver:  storeResolver{st: st},
		Workspace: scheduler.NewGitWorkspace(workspaceRoot, repoCache, "git"),
		Committer: scheduler.GitCommitter{GitBin: "git"},
		VCS:       scheduler.NewHTTPVCS(adminURL, adminToken),
		ModelEnv:  modelEnvFetcher(adminURL, adminToken),
	})
	if err != nil {
		log.Fatalf("[scheduler] init: %v", err)
	}

	go func() {
		if err := s.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[scheduler] loop stopped: %v", err)
		}
	}()

	app := scheduler.NewServer(scheduler.ServerConfig{
		Addr:  firstNonEmpty(os.Getenv("SCHEDULER_HTTP_ADDR"), ":8098"),
		Token: os.Getenv("SCHEDULER_TOKEN"),
	}, s)
	log.Printf("[scheduler] listening on %s (workspace=%s, max=%d/%d)", firstNonEmpty(os.Getenv("SCHEDULER_HTTP_ADDR"), ":8098"), workspaceRoot, cfg.MaxGlobal, cfg.MaxPerProject)
	if err := app.Listen(firstNonEmpty(os.Getenv("SCHEDULER_HTTP_ADDR"), ":8098")); err != nil {
		log.Fatalf("[scheduler] http: %v", err)
	}
}
