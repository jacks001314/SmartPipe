// release-service owns the durable release state machine and the executor
// pull API. It never opens inbound connections to build machines.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/releasesvc"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

var version = "dev"

func main() {
	dsn := strings.TrimSpace(os.Getenv("SMARTPIPE_DSN"))
	key := strings.TrimSpace(os.Getenv("SMARTPIPE_MASTER_KEY"))
	if dsn == "" || key == "" {
		log.Fatal("SMARTPIPE_DSN 与 SMARTPIPE_MASTER_KEY 必须配置")
	}
	box, err := secretbox.NewFromBase64(key)
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
	addr := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SERVICE_ADDR"))
	if addr == "" {
		addr = ":8098"
	}
	token := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SERVICE_TOKEN"))
	if token == "" {
		log.Println("警告: SMARTPIPE_RELEASE_SERVICE_TOKEN 为空，执行器接口无鉴权，仅限本地开发")
	}
	objects, mode, err := releasev2.ObjectStoreFromEnv(strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_STORAGE_DIR")))
	if err != nil {
		log.Fatalf("初始化制品存储失败: %v", err)
	}
	log.Printf("制品存储模式: %s", mode)
	maxAttempts := targetMaxAttempts()
	handler := &releasesvc.HTTPHandler{Store: st, Objects: objects, Token: token, MaxTargetAttempts: maxAttempts}
	log.Printf("release service: max target attempts=%d (retryable failures)", maxAttempts)
	srv := &http.Server{Addr: addr, Handler: handler.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("release-service %s 监听 %s", version, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()
	runCtx, stop := context.WithCancel(context.Background())
	runner := &releasesvc.Runner{Store: st, MaxActive: maxActiveRuns()}
	log.Printf("release service: max active runs=%d (0 = unlimited)", runner.MaxActive)
	go runner.Run(runCtx)
	if js, ok := objects.(releasesvc.ArtifactStore); ok && artifactGCEnabled() {
		janitor := &releasesvc.ArtifactJanitor{
			Objects: js, Runs: st,
			Interval: durationEnv("SMARTPIPE_RELEASE_ARTIFACT_GC_INTERVAL", 15*time.Minute),
			Grace:    durationEnv("SMARTPIPE_RELEASE_ARTIFACT_GC_GRACE", time.Hour),
			Retention: durationEnv("SMARTPIPE_RELEASE_ARTIFACT_RETENTION", 0),
		}
		log.Printf("release service: artifact gc enabled (interval=%s grace=%s retention=%s)", janitor.Interval, janitor.Grace, janitor.Retention)
		go janitor.Run(runCtx)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	stop()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown)
}

// targetMaxAttempts 读取可重试失败（对象存储不可用等）的最大尝试次数，缺省 3。
func targetMaxAttempts() int {
	raw := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_TARGET_MAX_ATTEMPTS"))
	if raw == "" {
		return 3
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("警告: SMARTPIPE_RELEASE_TARGET_MAX_ATTEMPTS=%q 无效，按 3 处理", raw)
		return 3
	}
	return n
}

// maxActiveRuns 读取并发上限；非法值按 0（不限制）处理并在日志里说明。
func maxActiveRuns() int {
	raw := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_MAX_ACTIVE_RUNS"))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("SMARTPIPE_RELEASE_MAX_ACTIVE_RUNS=%q is invalid, using 0 (unlimited)", raw)
		return 0
	}
	return n
}

// artifactGCEnabled 默认开启，SMARTPIPE_RELEASE_ARTIFACT_GC=off 关闭。
func artifactGCEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_ARTIFACT_GC"))) {
	case "off", "0", "false", "disabled":
		return false
	}
	return true
}

func durationEnv(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Printf("%s=%q is invalid, using %s", name, raw, def)
		return def
	}
	return d
}
