// Issue 管理服务（M1）：
//   - 内存存储（demo/单机；Postgres 实现见 deploy/postgres/schema.sql，随 M1 收尾接入）
//   - REST API：/api/issues（创建/检索/详情/评论/状态）
//
// 环境变量：ISSUE_HTTP_ADDR（默认 :8092）。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/issue"
)

func main() {
	addr := os.Getenv("ISSUE_HTTP_ADDR")
	if addr == "" {
		addr = ":8092"
	}
	st := issue.NewMemoryStore()

	app := iris.New()
	app.Get("/healthz", func(ctx iris.Context) {
		ctx.JSON(map[string]any{"status": "ok", "service": "smartpipe-issue", "store": "memory"})
	})
	issue.Register(app, st)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	log.Printf("[issue] http listening on %s (store=memory)", addr)
	if err := app.Listen(addr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
