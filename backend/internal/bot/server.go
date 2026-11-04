package bot

import (
	"github.com/kataras/iris/v12"
)

// NewServer 管理面 HTTP 服务：健康检查与后续管理接口（M1 极简）。
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
	return app
}

func dispatcherName(cfg Config) string {
	if cfg.OrchestratorURL != "" {
		return "orchestrator"
	}
	return "ack"
}
