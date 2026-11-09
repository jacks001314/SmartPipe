package scheduler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
)

// ServerConfig 是调度器内网服务的配置。
type ServerConfig struct {
	Addr  string
	Token string
}

// NewServer 暴露 bot / bridge 调用的内网接口。
func NewServer(cfg ServerConfig, s *Scheduler) *iris.Application {
	app := iris.New()
	app.Get("/healthz", func(ctx iris.Context) {
		ctx.JSON(map[string]any{"status": "ok", "service": "smartpipe-scheduler"})
	})
	guard := func(ctx iris.Context) {
		if cfg.Token == "" || ctx.GetHeader("X-Scheduler-Token") == cfg.Token {
			ctx.Next()
			return
		}
		ctx.StatusCode(http.StatusUnauthorized)
		_, _ = ctx.WriteString("unauthorized")
	}
	app.Post("/internal/scheduler/messages", guard, func(ctx iris.Context) {
		// 图片/文件以 base64 随消息过来，请求体放宽到 64MB，同时防止无限读取。
		ctx.Request().Body = http.MaxBytesReader(ctx.ResponseWriter(), ctx.Request().Body, 64<<20)
		var msg Inbound
		if err := ctx.ReadJSON(&msg); err != nil {
			ctx.StatusCode(http.StatusBadRequest)
			ctx.JSON(map[string]string{"error": "invalid json"})
			return
		}
		// 只有附件、没有文字的消息同样要交给模型理解。
		if strings.TrimSpace(msg.Text) == "" && len(msg.Attachments) == 0 {
			ctx.StatusCode(http.StatusBadRequest)
			ctx.JSON(map[string]string{"error": "text or attachments required"})
			return
		}
		// 模型调用可能持续几分钟，先返回 202，再异步处理。
		ctx.StatusCode(http.StatusAccepted)
		ctx.JSON(map[string]any{"ok": true})
		go func() {
			runCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			if err := s.HandleMessage(runCtx, msg); err != nil {
				log.Printf("[scheduler] message failed msgId=%s: %v", msg.PlatformMessageID, err)
			}
		}()
	})
	app.Post("/internal/scheduler/github", guard, func(ctx iris.Context) {
		var ev IssueEvent
		if err := ctx.ReadJSON(&ev); err != nil || ev.Event == "" {
			ctx.StatusCode(http.StatusBadRequest)
			ctx.JSON(map[string]string{"error": "event required"})
			return
		}
		ctx.StatusCode(http.StatusAccepted)
		ctx.JSON(map[string]any{"ok": true})
		go func() {
			runCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			if err := s.HandleIssueEvent(runCtx, ev); err != nil {
				log.Printf("[scheduler] github event failed %s/%s issue=%d: %v", ev.Event, ev.Action, ev.Issue, err)
			}
		}()
	})
	app.Get("/internal/scheduler/workers", guard, func(ctx iris.Context) {
		workers, err := s.Workers(ctx.Request().Context())
		if err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(map[string]any{"workers": workers})
	})
	return app
}

// Workers 返回全部 worker 状态，供看板与排障。
func (s *Scheduler) Workers(ctx context.Context) ([]*Worker, error) {
	return s.repo.ListWorkers(ctx)
}
