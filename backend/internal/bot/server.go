package bot

import (
	"github.com/kataras/iris/v12"

	"smartpipe/sdks/lanxin"
)

// ReplyRequest 编排层经 bot 内网接口发蓝信消息的请求体。
type ReplyRequest struct {
	ChatType string `json:"chatType"` // "p2p" | "group"
	TargetID string `json:"targetId"` // staffId 或 groupId
	RefMsgID string `json:"refMsgId,omitempty"`
	Text     string `json:"text"`
}

// NewServer 管理面 HTTP 服务：/healthz + /internal/reply（编排层回复入口）。
// 蓝信凭证只在本进程内使用，编排层经此接口间接发送（最小授权）。
func NewServer(cfg Config, h *Handler) *iris.Application {
	app := iris.New()
	app.Get("/healthz", func(ctx iris.Context) {
		ctx.JSON(map[string]any{
			"status":     "ok",
			"service":    "smartpipe-bot",
			"apiBase":    cfg.APIBase,
			"dispatcher": dispatcherName(cfg),
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
		if err := h.SendText(ctx, req.ChatType, req.TargetID, req.RefMsgID, req.Text); err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(map[string]any{"ok": true})
	})
	// 本地联调：直接注入一条蓝信事件，走与线上完全相同的解析/幂等/上报路径。
	app.Post("/internal/events", internalTokenGuard(cfg.InternalToken), func(ctx iris.Context) {
		var ev lanxin.Event
		if err := ctx.ReadJSON(&ev); err != nil || ev.Type == "" {
			ctx.StatusCode(400)
			ctx.JSON(map[string]string{"error": "bad request: type/data required"})
			return
		}
		if err := h.HandleEvent(ctx, ev); err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(map[string]any{"ok": true})
	})
	return app
}

func dispatcherName(cfg Config) string {
	if cfg.OrchestratorURL != "" {
		return "orchestrator"
	}
	return "ack"
}

func internalTokenGuard(token string) iris.Handler {
	return func(ctx iris.Context) {
		if token == "" || ctx.GetHeader("X-Internal-Token") == token {
			ctx.Next()
			return
		}
		ctx.StatusCode(401)
		_, _ = ctx.WriteString("unauthorized")
	}
}
