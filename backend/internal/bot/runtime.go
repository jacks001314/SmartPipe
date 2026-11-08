package bot

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/kataras/iris/v12"

	"smartpipe/sdks/lanxin"
)

var errBotNotFound = errors.New("bot: runtime not found")

// BotRuntime 保存多个机器人各自的 Handler，并用 botId 路由回执与诊断事件。
type BotRuntime struct {
	mu       sync.RWMutex
	handlers map[string]*Handler
	order    []string
}

func NewRuntime() *BotRuntime {
	return &BotRuntime{handlers: map[string]*Handler{}}
}

func (r *BotRuntime) Add(botID string, h *Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	botID = strings.TrimSpace(botID)
	if botID == "" {
		botID = "default"
	}
	if _, exists := r.handlers[botID]; !exists {
		r.order = append(r.order, botID)
	}
	r.handlers[botID] = h
}

func (r *BotRuntime) Remove(botID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	botID = strings.TrimSpace(botID)
	if botID == "" {
		botID = "default"
	}
	delete(r.handlers, botID)
	for i, id := range r.order {
		if id == botID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

func (r *BotRuntime) pick(botID string) (*Handler, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	botID = strings.TrimSpace(botID)
	if botID != "" {
		if h := r.handlers[botID]; h != nil {
			return h, nil
		}
		return nil, errBotNotFound
	}
	if len(r.order) == 0 {
		return nil, errBotNotFound
	}
	if h := r.handlers[r.order[0]]; h != nil {
		return h, nil
	}
	return nil, errBotNotFound
}

func (r *BotRuntime) SendText(ctx context.Context, botID, chatType, targetID, refMsgID, text string) error {
	h, err := r.pick(botID)
	if err != nil {
		return err
	}
	return h.SendText(ctx, chatType, targetID, refMsgID, text)
}

func (r *BotRuntime) HandleEvent(ctx context.Context, botID string, ev lanxin.Event) error {
	h, err := r.pick(botID)
	if err != nil {
		return err
	}
	return h.HandleEvent(ctx, ev)
}

func (r *BotRuntime) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// NewRuntimeServer 是多机器人进程的管理面：healthz、按 botId 回执、按 botId 注入事件。
func NewRuntimeServer(cfg Config, rt *BotRuntime) *iris.Application {
	app := iris.New()
	app.Get("/healthz", func(ctx iris.Context) {
		ctx.JSON(map[string]any{
			"status":     "ok",
			"service":    "smartpipe-bot",
			"apiBase":    cfg.APIBase,
			"dispatcher": dispatcherName(cfg),
			"bots":       rt.IDs(),
		})
	})
	app.Post("/internal/reply", internalTokenGuard(cfg.InternalToken), func(ctx iris.Context) {
		var req ReplyRequest
		if err := ctx.ReadJSON(&req); err != nil || req.Text == "" || req.TargetID == "" {
			ctx.StatusCode(400)
			ctx.JSON(map[string]string{"error": "bad request: targetId/text required"})
			return
		}
		if req.ChatType == "" {
			req.ChatType = "p2p"
		}
		if err := rt.SendText(ctx, req.BotID, req.ChatType, req.TargetID, req.RefMsgID, req.Text); err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(map[string]any{"ok": true})
	})
	app.Post("/internal/events", internalTokenGuard(cfg.InternalToken), func(ctx iris.Context) {
		var ev lanxin.Event
		if err := ctx.ReadJSON(&ev); err != nil || ev.Type == "" {
			ctx.StatusCode(400)
			ctx.JSON(map[string]string{"error": "bad request: type/data required"})
			return
		}
		if err := rt.HandleEvent(ctx, ctx.URLParam("bot_id"), ev); err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(map[string]any{"ok": true})
	})
	return app
}
