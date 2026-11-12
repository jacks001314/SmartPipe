// admin-api 是集中配置管理台的服务端进程：项目注册、凭据托管、连接测试与审计。
//
// 环境变量：
//
//	SMARTPIPE_DSN           Postgres 连接串（必填）
//	SMARTPIPE_MASTER_KEY    32 字节 base64 主密钥（必填；缺失时启动失败）
//	SMARTPIPE_ADMIN_ADDR    监听地址，默认 :8092
//	SMARTPIPE_ADMIN_TOKEN   管理员令牌；为空时以 dev-open 模式运行并打印告警
//	SMARTPIPE_CORS_ORIGINS  允许的前端来源，逗号分隔
//	SMARTPIPE_WEB_DIST      已构建的前端目录（可选，存在则提供静态站点）
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kataras/iris/v12"

	"path/filepath"

	"smartpipe/backend/internal/adminapi"
	"smartpipe/backend/internal/codexrunner"
	"smartpipe/backend/internal/conncheck"
	"smartpipe/backend/internal/releaseagent"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

var version = "0.1.0-m0"

func main() {
	dsn := strings.TrimSpace(os.Getenv("SMARTPIPE_DSN"))
	if dsn == "" {
		log.Fatal("SMARTPIPE_DSN 未设置，例如 postgres://smartpipe:pwd@192.168.1.99:5432/smartpipe?sslmode=disable")
	}
	keyB64 := strings.TrimSpace(os.Getenv("SMARTPIPE_MASTER_KEY"))
	if keyB64 == "-gen-key" {
		key, err := secretbox.GenerateKey()
		if err != nil {
			log.Fatalf("生成主密钥失败: %v", err)
		}
		fmt.Println(key)
		return
	}
	if keyB64 == "" {
		log.Fatal("SMARTPIPE_MASTER_KEY 未设置；可用 admin-api -gen-key 生成")
	}
	box, err := secretbox.NewFromBase64(keyB64)
	if err != nil {
		log.Fatalf("主密钥无效: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(ctx, dsn, box)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("执行迁移失败: %v", err)
	}

	addr := envOr("SMARTPIPE_ADMIN_ADDR", ":8092")
	adminToken := strings.TrimSpace(os.Getenv("SMARTPIPE_ADMIN_TOKEN"))
	if adminToken == "" {
		log.Println("警告: SMARTPIPE_ADMIN_TOKEN 为空，管理接口以 dev-open 模式运行，禁止用于生产")
	}
	origins := splitCSV(os.Getenv("SMARTPIPE_CORS_ORIGINS"))

	releaseAgent := newReleaseAgentLauncher(context.Background(), st)
	if releaseAgent != nil {
		if n, cerr := st.CloseStaleAgentSessions(context.Background()); cerr == nil && n > 0 {
			log.Printf("已把 %d 个遗留的发布 Agent 会话标记为失败", n)
		}
	}

	api := adminapi.New(adminapi.Config{
		ReleaseAgent:          releaseAgent,
		Store:                 st,
		Checker:               conncheck.New(),
		AdminToken:            adminToken,
		AllowOrigins:          origins,
		Version:               version,
		VerifyURL:             envOr("SMARTPIPE_VERIFY_URL", ""),
		VerifyToken:           strings.TrimSpace(os.Getenv("SMARTPIPE_VERIFY_TOKEN")),
		ReleaseURL:            envOr("SMARTPIPE_RELEASE_URL", ""),
		ReleaseToken:          strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_TOKEN")),
		BotAddr:               envOr("SMARTPIPE_BOT_ADDR", ""),
		BotToken:              strings.TrimSpace(os.Getenv("SMARTPIPE_BOT_INTERNAL_TOKEN")),
		SchedulerURL:          envOr("SMARTPIPE_SCHEDULER_URL", "http://127.0.0.1:8098"),
		SchedulerToken:        strings.TrimSpace(os.Getenv("SMARTPIPE_SCHEDULER_TOKEN")),
		CrashRetentionDays:    envInt("SMARTPIPE_CRASH_RETENTION_DAYS", 30),
		RejectedRetentionDays: envInt("SMARTPIPE_CRASH_REJECT_RETENTION_DAYS", 7),
		AdminUser:             envOr("SMARTPIPE_ADMIN_USER", "admin"),
		AdminPassword:         os.Getenv("SMARTPIPE_ADMIN_PASSWORD"),
		AdminPasswordReset:    strings.EqualFold(os.Getenv("SMARTPIPE_ADMIN_PASSWORD_RESET"), "true"),
		SessionTTL:            envDuration("SMARTPIPE_SESSION_TTL", 12*time.Hour),
		CookieSecure:          strings.EqualFold(os.Getenv("SMARTPIPE_COOKIE_SECURE"), "true"),
	})

	if err := api.Bootstrap(context.Background()); err != nil {
		log.Fatalf("初始化管理员账号失败: %v", err)
	}

	app := iris.New()
	app.Use(iris.Compression)
	app.Get("/healthz", func(c iris.Context) {
		_ = c.JSON(iris.Map{"status": "ok", "service": "admin-api", "version": version})
	})
	api.Register(app)

	if dist := strings.TrimSpace(os.Getenv("SMARTPIPE_WEB_DIST")); dist != "" {
		if info, statErr := os.Stat(dist); statErr == nil && info.IsDir() {
			// HTML 必须每次回源，避免升级后浏览器继续引用已经不存在的旧 bundle；
			// 带哈希的资源可以长期缓存。
			app.Use(func(c iris.Context) {
				path := c.Path()
				switch {
				case strings.HasPrefix(path, "/assets/"):
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
				case path == "/" || strings.HasSuffix(path, ".html"):
					c.Header("Cache-Control", "no-store, must-revalidate")
					c.Header("Pragma", "no-cache")
				}
				c.Next()
			})
			app.HandleDir("/", dist, iris.DirOptions{IndexName: "/index.html", ShowList: false, Compress: true})
			log.Printf("静态站点目录: %s", dist)
		} else {
			log.Printf("SMARTPIPE_WEB_DIST 指向的目录不存在: %s", dist)
		}
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Println("收到退出信号，开始优雅关闭")
		shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = app.Shutdown(shutdownCtx)
	}()

	if schedule := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SCHEDULE")); schedule != "" && schedule != "off" {
		api.StartReleaseScheduler(context.Background(), schedule)
	}
	if os.Getenv("SMARTPIPE_CRASH_RETENTION_SCHEDULE") != "off" {
		api.StartRetentionWorker(context.Background(), time.Duration(envInt("SMARTPIPE_CRASH_RETENTION_HOURS", 6))*time.Hour)
	}

	log.Printf("admin-api %s 监听 %s", version, addr)
	if err := app.Listen(addr, iris.WithOptimizations); err != nil && !errors.Is(err, iris.ErrServerClosed) {
		log.Fatalf("HTTP 服务异常退出: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envDuration(key string, def time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// newReleaseAgentLauncher 组装发布 Agent：MCP 地址、Codex 路径、模型供应商环境。
// 未设置 SMARTPIPE_RELEASE_AGENT_MCP_URL 或显式关闭时返回 nil。
func newReleaseAgentLauncher(ctx context.Context, st *store.Store) *releaseagent.Launcher {
	mcpURL := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_AGENT_MCP_URL"))
	if mcpURL == "" {
		mcpURL = strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_MCP_URL"))
	}
	if mcpURL == "" || strings.EqualFold(strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_AGENT")), "off") {
		log.Println("发布 Agent 未启用：缺少 SMARTPIPE_RELEASE_AGENT_MCP_URL 或 SMARTPIPE_RELEASE_AGENT=off")
		return nil
	}
	workRoot := envOr("SMARTPIPE_RELEASE_AGENT_WORK_ROOT", filepath.Join(os.TempDir(), "smartpipe-release-agent"))
	modelEnv := func(ctx context.Context, projectID string) []string {
		project, err := st.GetProject(ctx, projectID)
		if err != nil {
			log.Printf("发布 Agent 读取项目失败: %v", err)
			return nil
		}
		providerID := strings.TrimSpace(project.CodexModelProvider)
		if providerID == "" {
			providerID, _ = st.GetSetting(ctx, "codex_default_provider")
		}
		if providerID == "" {
			return nil
		}
		provider, err := st.GetModelProvider(ctx, providerID)
		if err != nil || !provider.Enabled {
			log.Printf("发布 Agent 模型供应商不可用: %v", err)
			return nil
		}
		_, key, _, err := st.GetSecret(ctx, provider.CredentialID)
		if err != nil {
			log.Printf("发布 Agent 读取模型凭据失败: %v", err)
			return nil
		}
		env := []string{}
		if provider.BaseURL != "" {
			env = append(env, "OPENAI_BASE_URL="+provider.BaseURL)
		}
		if key != "" {
			env = append(env, "OPENAI_API_KEY="+key)
		}
		return env
	}
	launcher := releaseagent.New(st, releaseagent.RunnerFunc(codexrunner.Run), releaseagent.Config{
		Enabled:       true,
		MCPURL:        mcpURL,
		ServerName:    envOr("SMARTPIPE_RELEASE_AGENT_MCP_NAME", "smartpipe_release"),
		TokenEnvVar:   envOr("SMARTPIPE_RELEASE_AGENT_TOKEN_ENV", "SMARTPIPE_RELEASE_MCP_TOKEN"),
		WorkRoot:      workRoot,
		CodexPath:     strings.TrimSpace(os.Getenv("SMARTPIPE_CODEX_PATH")),
		Model:         strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_AGENT_MODEL")),
		Effort:        strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_AGENT_EFFORT")),
		Sandbox:       envOr("SMARTPIPE_RELEASE_AGENT_SANDBOX", "read-only"),
		Compatibility: envOr("SMARTPIPE_RELEASE_AGENT_COMPAT", "warn"),
		Timeout:       time.Duration(envInt("SMARTPIPE_RELEASE_AGENT_TIMEOUT_MINUTES", 45)) * time.Minute,
		MaxSessions:   envInt("SMARTPIPE_RELEASE_AGENT_MAX_SESSIONS", 0),
		ModelEnv:      modelEnv,
	})
	_ = ctx
	log.Printf("发布 Agent 已启用：MCP %s，工作目录 %s", mcpURL, workRoot)
	return launcher
}
