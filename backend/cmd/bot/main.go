// 蓝信机器人服务（M1）：
//   - WebSocket 长连接接收蓝信消息（SDK 内置心跳/重连）
//   - 归一化 + msgId 幂等 + @过滤 + /new 会话重置
//   - 经 Dispatcher 交给编排层（当前为回执占位，Orchestrator 接入后切换）
//   - 启动时从配置台读取蓝信应用配置，运行期轮询变更并重连
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

type wsRunner struct {
	cancel  context.CancelFunc
	done    chan struct{}
	handler *bot.Handler
}

func startWS(parent context.Context, cfg bot.Config) *wsRunner {
	client := lanxin.New(cfg.AppID, cfg.AppSecret, lanxin.WithAPIBase(cfg.APIBase))
	handler := bot.NewHandler(cfg, client, bot.NewDispatcher(cfg, client))
	if cfg.CrashIngestURL != "" {
		handler = handler.WithCrash(&bot.CrashIntake{
			IngestURL: cfg.CrashIngestURL, Token: cfg.CrashToken, ProjectID: cfg.CrashProject,
			MaxBytes: cfg.CrashMaxBytes, Media: client,
		})
	}
	ws := lanxin.NewWSClient(client, handler.HandleEvent)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ws.Run(ctx); err != nil {
			log.Printf("ws loop exit: %v", err)
		}
	}()
	return &wsRunner{cancel: cancel, done: done, handler: handler}
}

func main() {
	cfg, err := bot.LoadFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	envCfg := cfg
	if cfg.AdminURL != "" {
		if rc, err := bot.FetchRemoteConfig(ctx, cfg.AdminURL, cfg.InternalToken); err != nil {
			log.Printf("[bot] 读取配置台失败，使用环境变量: %v", err)
		} else if rc != nil && rc.AppID != "" {
			cfg = cfg.WithRemote(rc)
			log.Printf("[bot] 已从配置台加载蓝信应用配置 (app_id=%s)", cfg.AppID)
		}
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	runner := startWS(ctx, cfg)

	if cfg.AdminURL != "" {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					rc, err := bot.FetchRemoteConfig(ctx, cfg.AdminURL, cfg.InternalToken)
					if err != nil || rc == nil {
						continue
					}
					target := envCfg
					if rc.AppID != "" {
						target = envCfg.WithRemote(rc)
					}
					if target.SameLanxin(rc) {
						continue
					}
					log.Printf("[bot] 检测到蓝信配置变更，重连 websocket")
					runner.cancel()
					<-runner.done
					cfg = target
					runner = startWS(ctx, cfg)
				}
			}
		}()
	}

	app := bot.NewServer(cfg, runner.handler)
	go func() {
		<-ctx.Done()
		runner.cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	log.Printf("[bot] http listening on %s (api=%s)", cfg.HTTPAddr, cfg.APIBase)
	if err := app.Listen(cfg.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
