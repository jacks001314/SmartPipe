package bot

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 机器人服务配置（全部来自环境变量）。
type Config struct {
	// BotID / BotName 标识配置台中的一个蓝信机器人。
	BotID   string
	BotName string

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
	// BridgeURL 非空且未配 OrchestratorURL 时，文本消息直接交给 bridge 建 GitHub Issue。
	BridgeURL   string
	BridgeToken string

	// RequireAtMe 群聊是否仅在 @机器人 时响应（默认 true）。
	RequireAtMe bool

	// AdminURL 非空时，启动与运行期会从配置台读取蓝信应用配置。
	AdminURL string

	// InternalToken 内网接口共享令牌（bot 与 orchestrator 一致；为空则不校验）。
	InternalToken string

	// CrashIngestURL 崩溃上报入口（如 http://127.0.0.1:8090/ingest/crash）；为空表示不开启一键上报。
	CrashIngestURL string
	// CrashToken 上报接口的 X-Ingest-Token。
	CrashToken string
	// CrashProject 群里没绑项目时的兜底 project_id。
	CrashProject string
	// CrashMaxBytes 附件大小上限（默认 256KB）。
	CrashMaxBytes int
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		AppID:           os.Getenv("LANXIN_APP_ID"),
		AppSecret:       os.Getenv("LANXIN_APP_SECRET"),
		APIBase:         getenv("LANXIN_API_BASE", "https://open.e.lanxin.cn/open/apigw"),
		HTTPAddr:        getenv("BOT_HTTP_ADDR", ":8091"),
		OrchestratorURL: os.Getenv("ORCHESTRATOR_URL"),
		BridgeURL:       os.Getenv("BRIDGE_URL"),
		BridgeToken:     os.Getenv("BRIDGE_TOKEN"),
		RequireAtMe:     getenv("BOT_REQUIRE_AT_ME", "true") != "false",
		InternalToken:   os.Getenv("INTERNAL_TOKEN"),
		CrashIngestURL:  os.Getenv("CRASH_INGEST_URL"),
		CrashToken:      os.Getenv("CRASH_INGEST_TOKEN"),
		CrashProject:    os.Getenv("CRASH_PROJECT_ID"),
		CrashMaxBytes:   atoiOr(os.Getenv("CRASH_MAX_BYTES"), 256*1024),
		AdminURL:        strings.TrimRight(os.Getenv("SMARTPIPE_ADMIN_URL"), "/"),
	}
	return cfg, nil
}

// Validate 在远程配置合并后检查必填项。
func (c Config) Validate() error {
	if c.AppID == "" || c.AppSecret == "" {
		return fmt.Errorf("bot: 蓝信 App ID / Secret 未配置（配置台「蓝信配置」或 LANXIN_APP_ID/LANXIN_APP_SECRET）")
	}
	return nil
}

func atoiOr(raw string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return n
	}
	return def
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
