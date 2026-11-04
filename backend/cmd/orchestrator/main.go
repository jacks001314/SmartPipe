// 编排服务（M1）：
//   - POST /internal/messages 接收 bot 转发的归一化消息
//   - Triage 规则引擎占位（相似检索 -> 归并/新建），后续替换为 Syntropy Triage Agent
//   - 经 Issue 服务归档，经 bot 内网接口回复蓝信
//
// 环境变量见 internal/orchestrator/config.go。
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"smartpipe/backend/internal/issue"
	"smartpipe/backend/internal/orchestrator"
)

func main() {
	cfg := orchestrator.LoadFromEnv()

	issues := issue.NewClient(cfg.IssueServiceURL)
	eng := &orchestrator.Orchestrator{
		Tri: &orchestrator.RuleTriage{
			Issues:           issues,
			MergeThreshold:   cfg.MergeThreshold,
			RelatedThreshold: cfg.RelatedThreshold,
		},
		Bot: &orchestrator.HTTPBotClient{BaseURL: cfg.BotURL, Token: cfg.InternalToken},
	}

	app := orchestrator.NewServer(cfg, eng)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	log.Printf("[orch] http listening on %s (issue=%s bot=%s)", cfg.HTTPAddr, cfg.IssueServiceURL, cfg.BotURL)
	if err := app.Listen(cfg.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
