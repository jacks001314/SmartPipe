// mcp-gateway 是 SmartPipe 的 MCP 工具网关：Agent 只持有项目级令牌，
// 由网关校验角色、限制工具集，并用服务端管理员令牌调用管理接口。
//
// 环境变量：
//
//	SMARTPIPE_MCP_ADDR      监听地址，默认 :8097
//	SMARTPIPE_ADMIN_URL     管理接口地址，默认 http://127.0.0.1:8092
//	SMARTPIPE_ADMIN_TOKEN   管理接口令牌（只在网关进程内使用）
//	SMARTPIPE_MCP_TOKEN     stdio 模式下使用的 Agent 令牌
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/mcp"
)

var version = "0.1.0"

func main() {
	stdio := flag.Bool("stdio", false, "以 stdio 方式运行（本地 MCP 客户端）")
	flag.Parse()

	adminURL := envOr("SMARTPIPE_ADMIN_URL", "http://127.0.0.1:8092")
	adminToken := strings.TrimSpace(os.Getenv("SMARTPIPE_ADMIN_TOKEN"))

	auth := func(ctx context.Context, token string) (*mcp.Principal, error) {
		return verifyToken(ctx, adminURL, adminToken, token)
	}
	server := &mcp.Server{
		Auth:     auth,
		Exec:     mcp.NewAdminBackend(adminURL, adminToken),
		Version:  version,
		Logger:   log.Default(),
		Recorder: newMCPRecorder(adminURL, adminToken, 256),
	}

	if *stdio {
		token := strings.TrimSpace(os.Getenv("SMARTPIPE_MCP_TOKEN"))
		if token == "" {
			log.Fatal("stdio 模式需要 SMARTPIPE_MCP_TOKEN")
		}
		if err := server.ServeStdio(context.Background(), os.Stdin, os.Stdout, token); err != nil {
			log.Fatalf("stdio 退出: %v", err)
		}
		return
	}

	addr := envOr("SMARTPIPE_MCP_ADDR", ":8097")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "smartpipe-mcp", "version": version})
	})
	mux.Handle("/mcp", server.Handler())

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("smartpipe-mcp 监听 %s（admin %s）", addr, adminURL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("MCP 网关退出: %v", err)
	}
}

func verifyToken(ctx context.Context, adminURL, adminToken, token string) (*mcp.Principal, error) {
	payload, _ := json.Marshal(map[string]string{"token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(adminURL, "/")+"/admin/agent-tokens/verify", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, mcp.ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("verify token: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Role      string  `json:"role"`
			ProjectID *string `json:"project_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &mcp.Principal{TokenID: out.Data.ID, Name: out.Data.Name, Role: out.Data.Role, ProjectID: out.Data.ProjectID}, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
