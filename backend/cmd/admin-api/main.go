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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/adminapi"
	"smartpipe/backend/internal/conncheck"
	rt "smartpipe/backend/internal/runtime"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

const version = "0.1.0-m0"

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

	runtimeData := envOr("SMARTPIPE_RUNTIME_DATA", "")
	runtimeManager := rt.NewManager()
	if runtimeData != "" {
		runtimeManager = rt.NewManagerWithState(filepath.Join(runtimeData, "instances.json"))
	}

	api := adminapi.New(adminapi.Config{
		Store:          st,
		Checker:        conncheck.New(),
		Runtime:        runtimeManager,
		AdminToken:     adminToken,
		AllowOrigins:   origins,
		Version:        version,
		AppServerBin:   envOr("SMARTPIPE_APPSERVER_BIN", defaultAppServerBinary()),
		RuntimeDataDir: runtimeData,
		AppServerToken: strings.TrimSpace(os.Getenv("SMARTPIPE_APPSERVER_TOKEN")),
		VerifyURL:      envOr("SMARTPIPE_VERIFY_URL", ""),
		VerifyToken:    strings.TrimSpace(os.Getenv("SMARTPIPE_VERIFY_TOKEN")),
		ReleaseURL:     envOr("SMARTPIPE_RELEASE_URL", ""),
		ReleaseToken:   strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_TOKEN")),
		BotAddr:        envOr("SMARTPIPE_BOT_ADDR", ""),
		BotToken:       strings.TrimSpace(os.Getenv("SMARTPIPE_BOT_INTERNAL_TOKEN")),
	})

	app := iris.New()
	app.Use(iris.Compression)
	app.Get("/healthz", func(c iris.Context) {
		_ = c.JSON(iris.Map{"status": "ok", "service": "admin-api", "version": version})
	})
	api.Register(app)

	if dist := strings.TrimSpace(os.Getenv("SMARTPIPE_WEB_DIST")); dist != "" {
		if info, statErr := os.Stat(dist); statErr == nil && info.IsDir() {
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

	log.Printf("admin-api %s 监听 %s", version, addr)
	if err := app.Listen(addr, iris.WithOptimizations); err != nil && !errors.Is(err, iris.ErrServerClosed) {
		log.Fatalf("HTTP 服务异常退出: %v", err)
	}
}

// defaultAppServerBinary 优先取同目录下的 syntropy-app-server[.exe]。
func defaultAppServerBinary() string {
	name := "syntropy-app-server"
	if strings.EqualFold(filepath.Ext(os.Args[0]), ".exe") {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
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
