package bot

import (
	"fmt"
	"os"
)

// Config 机器人服务配置（全部来自环境变量）。
type Config struct {
	// AppID / AppSecret 蓝信个人机器人凭证（桌面端：通讯录 -> 智能机器人 -> 个人机器人 -> ℹ️）。
	AppID     string
	AppSecret string

	// APIBase 蓝信 API 网关。公有云默认 https://open.e.lanxin.cn/open/apigw，
	// 企业私有部署必须显式设置 LANXIN_API_BASE。
	APIBase string

	// HTTPAddr Iris 监听地址（healthz / 管理接口）。
	HTTPAddr string

	// OrchestratorURL 为空时使用 AckDispatcher（本地回执占位）；
	// 非空时把归一化消息 POST 到 {OrchestratorURL}/internal/messages。
	OrchestratorURL string

	// RequireAtMe 群聊是否仅在 @机器人 时响应（默认 true）。
	RequireAtMe bool

	// InternalToken 内网接口共享令牌（bot 与 orchestrator 一致；为空则不校验）。
	InternalToken string
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		AppID:           os.Getenv("LANXIN_APP_ID"),
		AppSecret:       os.Getenv("LANXIN_APP_SECRET"),
		APIBase:         getenv("LANXIN_API_BASE", "https://open.e.lanxin.cn/open/apigw"),
		HTTPAddr:        getenv("BOT_HTTP_ADDR", ":8091"),
		OrchestratorURL: os.Getenv("ORCHESTRATOR_URL"),
		RequireAtMe:     getenv("BOT_REQUIRE_AT_ME", "true") != "false",
		InternalToken:   os.Getenv("INTERNAL_TOKEN"),
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return cfg, fmt.Errorf("bot: LANXIN_APP_ID / LANXIN_APP_SECRET 必填（蓝信桌面端获取）")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
