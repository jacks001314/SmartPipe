// 蓝信机器人服务（M1）：
//   - WebSocket 长连接接收蓝信消息（SDK 内置心跳/重连）
//   - 归一化 + msgId 幂等 + @过滤 + /new 会话重置
//   - 经 Dispatcher 交给编排层（当前为回执占位，Orchestrator 接入后切换）
//   - Iris 管理面：/healthz
//
// 环境变量见 internal/bot/config.go。
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"smartpipe/backend/internal/bot"
	"smartpipe/sdks/lanxin"
)

func main() {
	cfg, err := bot.LoadFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	client := lanxin.New(cfg.AppID, cfg.AppSecret, lanxin.WithAPIBase(cfg.APIBase))
	handler := bot.NewHandler(cfg, client, bot.NewDispatcher(cfg, client))
	if cfg.CrashIngestURL != "" {
		handler = handler.WithCrash(&bot.CrashIntake{
			IngestURL: cfg.CrashIngestURL, Token: cfg.CrashToken, ProjectID: cfg.CrashProject,
			MaxBytes: cfg.CrashMaxBytes, Media: client,
		})
	}
	ws := lanxin.NewWSClient(client, handler.HandleEvent)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := ws.Run(ctx); err != nil {
			log.Printf("ws loop exit: %v", err)
		}
	}()

	app := bot.NewServer(cfg, handler)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	log.Printf("[bot] http listening on %s (api=%s)", cfg.HTTPAddr, cfg.APIBase)
	if err := app.Listen(cfg.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
