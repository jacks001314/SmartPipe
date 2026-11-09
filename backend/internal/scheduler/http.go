package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
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
	app.Get("/internal/scheduler/overview", guard, func(ctx iris.Context) {
		overview, err := s.Overview(ctx.Request().Context())
		if err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(overview)
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
	app.Get("/internal/scheduler/workers/{id}", guard, func(ctx iris.Context) {
		detail, err := s.WorkerDetail(ctx.Request().Context(), ctx.Params().Get("id"))
		if err != nil {
			writeSchedulerError(ctx, err)
			return
		}
		ctx.JSON(detail)
	})
	app.Get("/internal/scheduler/workers/{id}/output", guard, func(ctx iris.Context) {
		frame, err := s.WorkerOutputs(ctx.Request().Context(), ctx.Params().Get("id"),
			queryInt64(ctx, "after", 0), queryInt(ctx, "limit", 1000))
		if err != nil {
			writeSchedulerError(ctx, err)
			return
		}
		ctx.JSON(frame)
	})
	app.Get("/internal/scheduler/workers/{id}/output/{item}/raw", guard, func(ctx iris.Context) {
		page, err := s.WorkerOutputRaw(ctx.Request().Context(), ctx.Params().Get("id"), ctx.Params().Get("item"),
			queryInt64(ctx, "offset", 0), queryInt64(ctx, "limit", outputPageBytes))
		if err != nil {
			writeSchedulerError(ctx, err)
			return
		}
		ctx.JSON(page)
	})
	app.Get("/internal/scheduler/workers/{id}/stream", guard, func(ctx iris.Context) {
		serveWorkerOutputStream(ctx, s)
	})
	app.Post("/internal/scheduler/workers/{id}/action", guard, func(ctx iris.Context) {
		var in struct {
			Action string `json:"action"`
		}
		if err := ctx.ReadJSON(&in); err != nil || strings.TrimSpace(in.Action) == "" {
			ctx.StatusCode(http.StatusBadRequest)
			ctx.JSON(map[string]string{"error": "action required"})
			return
		}
		w, err := s.Act(ctx.Request().Context(), ctx.Params().Get("id"), in.Action)
		if err != nil {
			writeSchedulerError(ctx, err)
			return
		}
		ctx.JSON(map[string]any{"worker": w})
	})
	return app
}

func writeSchedulerError(ctx iris.Context, err error) {
	switch {
	case errors.Is(err, ErrWorkerNotFound):
		ctx.StatusCode(http.StatusNotFound)
	case errors.Is(err, ErrActionNotAllowed):
		ctx.StatusCode(http.StatusConflict)
	default:
		ctx.StatusCode(http.StatusInternalServerError)
	}
	ctx.JSON(map[string]string{"error": err.Error()})
}

func queryInt64(ctx iris.Context, key string, fallback int64) int64 {
	raw := strings.TrimSpace(ctx.URLParam(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func queryInt(ctx iris.Context, key string, fallback int) int {
	n := queryInt64(ctx, key, int64(fallback))
	if n <= 0 || n > 1000 {
		return fallback
	}
	return int(n)
}

// serveWorkerOutputStream 用 revision 游标增量推送。连接断开后客户端带最后
// revision 重连即可补齐，服务端不维护易失的广播队列。首个事件可能稍后才出现，
// 因此空闲时持续发送 keep-alive 注释帧，避免代理和休眠客户端留下半开连接。
func serveWorkerOutputStream(ctx iris.Context, s *Scheduler) {
	id := strings.TrimSpace(ctx.Params().Get("id"))
	if id == "" {
		ctx.StatusCode(http.StatusBadRequest)
		return
	}
	flusher, ok := ctx.ResponseWriter().Flusher()
	if !ok {
		ctx.StatusCode(http.StatusHTTPVersionNotSupported)
		_, _ = ctx.WriteString("streaming unsupported")
		return
	}
	ctx.ContentType("text/event-stream")
	ctx.Header("Cache-Control", "no-cache, no-transform")
	ctx.Header("Connection", "keep-alive")
	ctx.Header("X-Accel-Buffering", "no")

	cursor := queryInt64(ctx, "after", 0)
	if cursor == 0 {
		if raw := strings.TrimSpace(ctx.GetHeader("Last-Event-ID")); raw != "" {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
				cursor = n
			}
		}
	}

	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	lastWrite := time.Now()
	for {
		frame, err := s.WorkerOutputs(ctx.Request().Context(), id, cursor, 500)
		if err != nil {
			writeSchedulerError(ctx, err)
			return
		}
		if len(frame.Items) > 0 {
			cursor = frame.Cursor
			payload, _ := json.Marshal(frame)
			if _, err := fmt.Fprintf(ctx.ResponseWriter(), "id: %d\nevent: outputs\ndata: %s\n\n", cursor, payload); err != nil {
				return
			}
			flusher.Flush()
			lastWrite = time.Now()
		}
		if !frame.Running {
			worker, err := s.loadWorker(ctx.Request().Context(), id)
			if err == nil && (worker.State == StateClosed || worker.State == StateFailed) {
				_, _ = fmt.Fprintf(ctx.ResponseWriter(), "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
		}
		select {
		case <-ctx.Request().Context().Done():
			return
		case <-ticker.C:
			if time.Since(lastWrite) > 15*time.Second {
				if _, err := fmt.Fprint(ctx.ResponseWriter(), ": keep-alive\n\n"); err != nil {
					return
				}
				flusher.Flush()
				lastWrite = time.Now()
			}
		}
	}
}
