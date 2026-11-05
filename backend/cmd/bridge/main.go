// bridge 是蓝信 ↔ GitHub Issue 的桥接服务。
//
// 环境变量：
//
//	SMARTPIPE_DSN              Postgres 连接串（必填）
//	SMARTPIPE_MASTER_KEY       32 字节 base64 主密钥（必填）
//	SMARTPIPE_BRIDGE_ADDR      监听地址，默认 :8090
//	SMARTPIPE_BRIDGE_TOKEN     调用 /bridge/* 的内部令牌（为空则不校验）
//	GITHUB_WEBHOOK_SECRET      Webhook 签名密钥（为空时取凭据库里的全局 github_webhook_secret）
//	SMARTPIPE_APPSERVER_ADDR   启用 lead 派单时 App Server 地址（按项目绑定里的地址优先）
//	SMARTPIPE_APPSERVER_TOKEN  App Server 桌面会话令牌
//	SMARTPIPE_BOT_ADDR         bot 内网地址，设置后把 issue_url 回执到蓝信会话
//	SMARTPIPE_BOT_INTERNAL_TOKEN bot /internal/reply 的内部令牌
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/syntropyclient"
)

func main() {
	dsn := strings.TrimSpace(os.Getenv("SMARTPIPE_DSN"))
	if dsn == "" {
		log.Fatal("SMARTPIPE_DSN 未设置")
	}
	box, err := secretbox.NewFromBase64(strings.TrimSpace(os.Getenv("SMARTPIPE_MASTER_KEY")))
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
		log.Fatalf("迁移失败: %v", err)
	}

	secret := strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET"))
	if secret == "" {
		secret = lookupWebhookSecret(ctx, st)
	}
	if secret == "" {
		log.Println("警告: 未配置 webhook secret，/webhooks/github 将跳过签名校验（仅限本地开发）")
	}

	svc := bridge.NewService(st, nil)
	if addr := strings.TrimSpace(os.Getenv("SMARTPIPE_APPSERVER_ADDR")); addr != "" {
		lead := bridge.NewLeadDispatcher(st)
		lead.Token = strings.TrimSpace(os.Getenv("SMARTPIPE_APPSERVER_TOKEN"))
		svc.WithDispatcher(lead)
		log.Printf("启用 lead 派单，默认 App Server %s", addr)
	}
	if addr := strings.TrimSpace(os.Getenv("SMARTPIPE_BOT_ADDR")); addr != "" {
		svc.WithNotifier(bridge.NewBotNotifier(addr, strings.TrimSpace(os.Getenv("SMARTPIPE_BOT_INTERNAL_TOKEN"))))
		log.Printf("启用蓝信回执，bot %s", addr)
	}

	approvals := &bridge.ApprovalService{Store: st}
	appToken := strings.TrimSpace(os.Getenv("SMARTPIPE_APPSERVER_TOKEN"))
	approvals.NotifyLead = func(ctx context.Context, projectID, text string) error {
		b, err := st.GetBinding(ctx, projectID)
		if err != nil {
			return err
		}
		agentProject := strings.TrimSpace(b.AgentProjectID)
		if agentProject == "" {
			agentProject = b.ProjectID
		}
		_, err = syntropyclient.New(b.AppServerAddr, appToken).SendAgentMessage(ctx, agentProject, b.LeadAgentID, text)
		return err
	}
	webhook := &bridge.WebhookHandler{Store: st, Secret: secret, Approvals: approvals}
	bridgeToken := strings.TrimSpace(os.Getenv("SMARTPIPE_BRIDGE_TOKEN"))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "bridge"})
	})
	mux.Handle("/webhooks/github", webhook)
	mux.HandleFunc("/bridge/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if bridgeToken != "" && r.Header.Get("X-Bridge-Token") != bridgeToken {
			http.Error(w, "invalid bridge token", http.StatusUnauthorized)
			return
		}
		var msg bridge.Message
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&msg); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		link, created, err := svc.HandleMessage(r.Context(), msg)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"link": link, "created": created})
	})
	mux.HandleFunc("/bridge/links", func(w http.ResponseWriter, r *http.Request) {
		items, err := st.ListBridgeLinks(r.Context(), 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items})
	})

	addr := strings.TrimSpace(os.Getenv("SMARTPIPE_BRIDGE_ADDR"))
	if addr == "" {
		addr = ":8090"
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("bridge 监听 %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("bridge 退出: %v", err)
	}
}

func lookupWebhookSecret(ctx context.Context, st *store.Store) string {
	items, err := st.ListCredentials(ctx, nil)
	if err != nil {
		return ""
	}
	for _, c := range items {
		if c.Enabled && c.Kind == store.CredentialGitHubHook {
			if _, secret, _, err := st.GetSecret(ctx, c.ID); err == nil {
				return secret
			}
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
