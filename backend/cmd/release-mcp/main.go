// release-mcp exposes the v2 release tools to a dedicated Codex Release Agent.
//
// Environment:
//
//	SMARTPIPE_DSN          PostgreSQL DSN (required)
//	SMARTPIPE_MASTER_KEY   32-byte base64 credential key (required)
//	SMARTPIPE_RELEASE_MCP_ADDR  listen address, default :8097
//	SMARTPIPE_RELEASE_STORAGE_DIR  artifact storage root, default ./data/releases
//	SMARTPIPE_RELEASE_OBJECTS_PUBLIC_URL  public base URL serving the storage root
//	SMARTPIPE_RELEASE_UPDATE_URL  default upgrade service base URL
//	SMARTPIPE_CRED_<REF>  external credentials resolved by reference
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/releasemcp"
	"smartpipe/backend/internal/releasepublish"
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
	addr := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_MCP_ADDR"))
	if addr == "" {
		addr = ":8097"
	}
	service := releasev2.NewService(st)
	mcp := releasemcp.New(st, service, version)
	objects, mode, err := releasev2.ObjectStoreFromEnv(strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_STORAGE_DIR")))
	if err != nil {
		log.Fatalf("初始化制品存储失败: %v", err)
	}
	log.Printf("制品存储模式: %s", mode)
	mcp.Objects = objects
	if presigner, ok := objects.(releasev2.Presigner); ok {
		mcp.Presigner = presigner
	}
	mcp.UpdateURL = strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_UPDATE_URL"))
	mcp.GitHubCredential = strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_GITHUB_CREDENTIAL"))
	mcp.UpdateCredential = strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_UPDATE_CREDENTIAL"))
	if apiBase := strings.TrimSpace(os.Getenv("SMARTPIPE_GITHUB_API_BASE")); apiBase != "" {
		mcp.GitHub = &releasepublish.GitHubClient{APIBase: apiBase, UploadBase: apiBase}
	}
	srv := &http.Server{Addr: addr, Handler: mcp.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("release-mcp %s 监听 %s", version, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown)
}
