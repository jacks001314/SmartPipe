package orchestrator

import (
	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/bot"
)

// NewServer 编排服务 HTTP 面：/healthz + /internal/messages（bot 转发入口）。
func NewServer(cfg Config, o *Orchestrator) *iris.Application {
	app := iris.New()
	app.Get("/healthz", func(ctx iris.Context) {
		ctx.JSON(map[string]any{"status": "ok", "service": "smartpipe-orchestrator"})
	})
	app.Post("/internal/messages", internalTokenGuard(cfg.InternalToken), func(ctx iris.Context) {
		var msg bot.Inbound
		if err := ctx.ReadJSON(&msg); err != nil || msg.PlatformMessageID == "" {
			ctx.StatusCode(400)
			ctx.JSON(map[string]string{"error": "bad inbound message"})
			return
		}
		if err := o.HandleInbound(ctx, msg); err != nil {
			ctx.StatusCode(500)
			ctx.JSON(map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(map[string]any{"ok": true})
	})
	return app
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
