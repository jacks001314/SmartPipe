// bridge 是蓝信 ↔ GitHub Issue 的桥接服务。
//
// 环境变量：
//
//	SMARTPIPE_DSN              Postgres 连接串（必填）
//	SMARTPIPE_MASTER_KEY       32 字节 base64 主密钥（必填）
//	SMARTPIPE_BRIDGE_ADDR      监听地址，默认 :8090
//	SMARTPIPE_BRIDGE_TOKEN     调用 /bridge/* 的内部令牌（为空则不校验）
//	GITHUB_WEBHOOK_SECRET      Webhook 签名密钥（为空时取凭据库里的全局 github_webhook_secret）
//	SMARTPIPE_BOT_ADDR         bot 内网地址，设置后把 issue_url 回执到蓝信会话
//	SMARTPIPE_BOT_INTERNAL_TOKEN bot /internal/reply 的内部令牌
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/crash"
	"smartpipe/backend/internal/frptunnel"
	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
)

// resolveProjectByChat 返回可用的项目 ID：优先用调用方给的 project_id，
// 找不到时按蓝信群/会话反查（管理员在配置台维护的 bluex_group_ids）。
func resolveProjectByChat(ctx context.Context, st *store.Store, projectID, botID, chatID string) string {
	if id := strings.TrimSpace(projectID); id != "" {
		if _, err := st.GetProject(ctx, id); err == nil {
			return id
		}
	}
	if p, err := st.FindProjectByGroupBot(ctx, botID, chatID); err == nil {
		return p.ID
	}
	return strings.TrimSpace(projectID)
}
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
	var botNotifier bridge.Notifier
	if addr := strings.TrimSpace(os.Getenv("SMARTPIPE_BOT_ADDR")); addr != "" {
		botNotifier = bridge.NewBotNotifier(addr, strings.TrimSpace(os.Getenv("SMARTPIPE_BOT_INTERNAL_TOKEN")))
		svc.WithNotifier(botNotifier)
		log.Printf("启用蓝信回执，bot %s", addr)
	}

	approvals := &bridge.ApprovalService{Store: st, GroupNotify: botNotifier}
	webhook := &bridge.WebhookHandler{Store: st, Secret: secret, Approvals: approvals,
		ForwardURL:   strings.TrimSpace(os.Getenv("SMARTPIPE_SCHEDULER_URL")),
		ForwardToken: strings.TrimSpace(os.Getenv("SCHEDULER_TOKEN")),
	}
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
		if p := resolveProjectByChat(r.Context(), st, msg.ProjectID, msg.BotID, msg.ChatID); p != "" {
			msg.ProjectID = p
		}

		// 先把入站事实写进收件箱，再建单；未绑定或建单失败也留痕，控制台可见。
		var projectPtr *string
		if msg.ProjectID != "" {
			projectPtr = &msg.ProjectID
		}
		if _, recErr := st.RecordLanxinInbound(r.Context(), store.LanxinMessage{
			PlatformMessageID: msg.MessageID, BotID: msg.BotID, BotName: msg.BotName,
			ChatType:   msg.ChatType,
			ChatID:     msg.ChatID,
			UserID:     msg.UserID,
			SenderName: msg.SenderName,
			GroupName:  msg.GroupName,
			MsgType:    "text",
			Text:       msg.Body,
			IsAtMe:     true,
			ProjectID:  projectPtr,
			Status:     store.LanxinStatusReceived,
		}); recErr != nil {
			log.Printf("[bridge] record lanxin inbound: %v", recErr)
		}
		if msg.ProjectID == "" {
			_ = st.MarkLanxinMessage(r.Context(), msg.MessageID, store.LanxinStatusUnbound, "群/会话未绑定项目", 0, "")
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有匹配到项目：请在项目创建/编辑页选择蓝信机器人及群"})
			return
		}

		res, err := svc.HandleInbound(r.Context(), msg)
		if err != nil {
			_ = st.MarkLanxinMessage(r.Context(), msg.MessageID, store.LanxinStatusFailed, err.Error(), 0, "")
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if res.Draft != nil && res.Link == nil {
			_ = st.MarkLanxinMessage(r.Context(), msg.MessageID, store.LanxinStatusReceived, "待群内确认建单", 0, "")
			writeJSON(w, http.StatusAccepted, map[string]any{"draft": res.Draft, "created": false})
			return
		}
		if res.Link == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		_ = st.MarkLanxinMessage(r.Context(), msg.MessageID, store.LanxinStatusIssueCreated, "", res.Link.IssueNumber, res.Link.IssueURL)
		status := http.StatusOK
		if res.Created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"link": res.Link, "created": res.Created})
	})
	// /bridge/inbound 记录未进入建单流程的消息（未 @、非文本、命令等），保证收件箱完整。
	mux.HandleFunc("/bridge/inbound", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if bridgeToken != "" && r.Header.Get("X-Bridge-Token") != bridgeToken {
			http.Error(w, "invalid bridge token", http.StatusUnauthorized)
			return
		}
		var in struct {
			MessageID  string `json:"message_id"`
			BotID      string `json:"bot_id"`
			BotName    string `json:"bot_name"`
			ChatType   string `json:"chat_type"`
			ChatID     string `json:"chat_id"`
			UserID     string `json:"user_id"`
			SenderName string `json:"sender_name"`
			GroupName  string `json:"group_name"`
			MsgType    string `json:"msg_type"`
			Text       string `json:"text"`
			IsAtMe     bool   `json:"is_at_me"`
			Reason     string `json:"reason"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(in.MessageID) == "" {
			http.Error(w, "message_id is required", http.StatusBadRequest)
			return
		}
		projectID := resolveProjectByChat(r.Context(), st, "", in.BotID, in.ChatID)
		var projectPtr *string
		if projectID != "" {
			projectPtr = &projectID
		}
		status := store.LanxinStatusReceived
		if strings.TrimSpace(in.Reason) != "" {
			status = store.LanxinStatusFiltered
		}
		rec, err := st.RecordLanxinInbound(r.Context(), store.LanxinMessage{
			PlatformMessageID: in.MessageID, BotID: in.BotID, BotName: in.BotName,
			ChatType:   in.ChatType,
			ChatID:     in.ChatID,
			UserID:     in.UserID,
			SenderName: in.SenderName,
			GroupName:  in.GroupName,
			MsgType:    in.MsgType,
			Text:       in.Text,
			IsAtMe:     in.IsAtMe,
			ProjectID:  projectPtr,
			Status:     status,
			Reason:     strings.TrimSpace(in.Reason),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": rec})
	})

	crashSvc := &crash.Service{
		Store: st,
		Forge: func(ctx context.Context, projectID *string) (crash.Forge, error) {
			return bridge.ClientFromCredentials(ctx, st, projectID)
		},
		MaxBytes:     envInt("SMARTPIPE_CRASH_MAX_BYTES", 256*1024),
		RateLimit:    envInt("SMARTPIPE_CRASH_RATE_LIMIT", 5),
		RateWindow:   time.Hour,
		DedupeWindow: 24 * time.Hour,
	}
	crashToken := strings.TrimSpace(os.Getenv("SMARTPIPE_CRASH_TOKEN"))
	if crashToken == "" {
		crashToken = bridgeToken
	}
	defaultCrashProject := strings.TrimSpace(os.Getenv("SMARTPIPE_CRASH_PROJECT"))
	mux.HandleFunc("/ingest/crash", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if crashToken != "" && r.Header.Get("X-Ingest-Token") != crashToken {
			http.Error(w, "invalid ingest token", http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		var in struct {
			ProjectID  string       `json:"project_id"`
			ChatID     string       `json:"chat_id"`
			MessageID  string       `json:"message_id"`
			BotID      string       `json:"bot_id"`
			BotName    string       `json:"bot_name"`
			GroupName  string       `json:"group_name"`
			SenderID   string       `json:"sender_id"`
			SenderName string       `json:"sender_name"`
			Source     string       `json:"source"`
			Bundle     crash.Bundle `json:"bundle"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		projectID := strings.TrimSpace(in.ProjectID)
		if projectID == "" {
			projectID = defaultCrashProject
		}
		// 配置漂移或机器人未配项目时，按蓝信群反查项目，避免整条上报链路 404。
		if p := resolveProjectByChat(r.Context(), st, projectID, "", in.ChatID); p != "" {
			projectID = p
		}
		if projectID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "project_id is required"})
			return
		}
		project, err := st.GetProject(r.Context(), projectID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "project not found"})
			return
		}
		repo := strings.TrimSpace(project.GitHubIssueRepo)
		if repo == "" {
			repo = strings.TrimSpace(project.GitHubRepoURL)
		}
		outcome, err := crashSvc.Ingest(r.Context(), crash.Ingest{
			Project: project, Repo: repo, Source: in.Source, Bundle: in.Bundle, RawSize: len(raw),
			MessageID: in.MessageID, ChatID: in.ChatID, BotID: in.BotID, BotName: in.BotName,
			GroupName: in.GroupName, SenderID: in.SenderID, SenderName: in.SenderName,
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		payload := map[string]any{"data": outcome}
		status := http.StatusCreated
		switch outcome.Status {
		case "awaiting_confirmation":
			status = http.StatusAccepted
		case "deduped":
			status = http.StatusOK
		case "rate_limited":
			status = http.StatusTooManyRequests
		case "rejected":
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, payload)
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

	// 内嵌 frpc：隧道参数来自配置页（settings 表），环境变量优先；
	// bridge 每 30 秒比对一次配置，改动后自动重建隧道。
	tunnelCtx, tunnelCancel := context.WithCancel(context.Background())
	defer tunnelCancel()
	go watchTunnel(tunnelCtx, st, addr)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		tunnelCancel()
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

// listenPort 从 ":8090" / "127.0.0.1:8090" 这类监听地址里取出端口。
func listenPort(addr string) int {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if port, err := strconv.Atoi(addr[i+1:]); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// tunnelSpec 是一份完整的隧道配置，来自环境变量或配置页。
type tunnelSpec struct {
	enabled    bool
	server     string
	serverPort int
	localPort  int
	remotePort int
	proxyName  string
	token      string
}

func (s tunnelSpec) key() string {
	return fmt.Sprintf("%t|%s|%d|%d|%d|%s|%x", s.enabled, s.server, s.serverPort,
		s.localPort, s.remotePort, s.proxyName, sha256.Sum256([]byte(s.token)))
}

func (s tunnelSpec) usable() bool {
	return s.enabled && strings.TrimSpace(s.server) != "" && s.token != ""
}

// resolveTunnelSpec 合并配置页与 SMARTPIPE_FRP_* 环境变量。
// 配置页优先，环境变量只在配置页留空时兜底，这样前端改完能真正生效。
func resolveTunnelSpec(ctx context.Context, st *store.Store, listenAddr string) tunnelSpec {
	spec := tunnelSpec{
		serverPort: envInt("SMARTPIPE_FRP_SERVER_PORT", 7000),
		localPort:  envInt("SMARTPIPE_FRP_LOCAL_PORT", listenPort(listenAddr)),
		remotePort: envInt("SMARTPIPE_FRP_REMOTE_PORT", 18090),
		proxyName:  firstNonEmpty(os.Getenv("SMARTPIPE_FRP_PROXY_NAME"), "smartpipe-webhook"),
	}
	if v := strings.TrimSpace(os.Getenv("SMARTPIPE_FRP_SERVER")); v != "" {
		spec.server = v
		spec.enabled = true
	}

	if v, err := st.GetSetting(ctx, "github_tunnel_server"); err == nil && strings.TrimSpace(v) != "" {
		spec.server = strings.TrimSpace(v)
	}
	if v, err := st.GetSetting(ctx, "github_tunnel_enabled"); err == nil && strings.TrimSpace(v) != "" {
		spec.enabled = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, err := st.GetSetting(ctx, "github_tunnel_server_port"); err == nil {
		if port, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil && port > 0 {
			spec.serverPort = port
		}
	}
	if v, err := st.GetSetting(ctx, "github_tunnel_remote_port"); err == nil {
		if port, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil && port > 0 {
			spec.remotePort = port
		}
	}
	if v, err := st.GetSetting(ctx, "github_tunnel_proxy_name"); err == nil && strings.TrimSpace(v) != "" {
		spec.proxyName = strings.TrimSpace(v)
	}

	// token 以配置页保存的凭据为准，没有凭据才退回环境变量。
	spec.token = lookupCredentialSecret(ctx, st, store.CredentialFRPToken)
	if spec.token == "" {
		spec.token = strings.TrimSpace(os.Getenv("SMARTPIPE_FRP_TOKEN"))
	}
	return spec
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

// watchTunnel 保证进程内始终跑着「当前配置」对应的隧道：配置变化即重建。
func watchTunnel(ctx context.Context, st *store.Store, listenAddr string) {
	var (
		cancel context.CancelFunc
		key    string
	)
	apply := func() {
		spec := resolveTunnelSpec(ctx, st, listenAddr)
		next := spec.key()
		if next == key {
			return
		}
		key = next
		if cancel != nil {
			cancel()
			cancel = nil
		}
		if !spec.usable() {
			if spec.enabled {
				log.Printf("内嵌 frpc 配置不完整（缺 frps 地址或 token），暂不启动")
			}
			return
		}
		runCtx, runCancel := context.WithCancel(ctx)
		cancel = runCancel
		log.Printf("启用内嵌 frpc: %s:%d -> 127.0.0.1:%d (remote %d, proxy %s)",
			spec.server, spec.serverPort, spec.localPort, spec.remotePort, spec.proxyName)
		go func() {
			if err := frptunnel.Run(runCtx, frptunnel.Config{
				ServerAddr: spec.server,
				ServerPort: spec.serverPort,
				Token:      spec.token,
				LocalPort:  spec.localPort,
				RemotePort: spec.remotePort,
				ProxyName:  spec.proxyName,
				LogLevel:   strings.TrimSpace(os.Getenv("SMARTPIPE_FRP_LOG_LEVEL")),
			}); err != nil && runCtx.Err() == nil {
				log.Printf("内嵌 frpc 退出: %v", err)
			}
		}()
	}
	apply()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if cancel != nil {
				cancel()
			}
			return
		case <-ticker.C:
			apply()
		}
	}
}

// lookupCredentialSecret 返回某一类全局凭据里第一个启用项的明文。
func lookupCredentialSecret(ctx context.Context, st *store.Store, kind string) string {
	items, err := st.ListCredentials(ctx, nil)
	if err != nil {
		return ""
	}
	for _, c := range items {
		if c.Enabled && c.Kind == kind {
			if _, secret, _, err := st.GetSecret(ctx, c.ID); err == nil {
				return secret
			}
		}
	}
	return ""
}
