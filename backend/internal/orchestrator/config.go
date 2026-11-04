// Package orchestrator 实现编排服务：
// 接收 bot 转发的归一化消息 -> Triage（M1 规则引擎占位，后续替换为
// Syntropy Triage Agent，接口位见 TriageEngine）-> Issue 归档 -> 蓝信回复。
// 设计见 docs/自动化开发-测试-发版管道设计方案.md 3.4/9.3 节。
package orchestrator

import (
	"fmt"
	"os"
)

// Config 编排服务配置（全部来自环境变量）。
type Config struct {
	HTTPAddr         string  // 默认 :8090
	IssueServiceURL  string  // 默认 http://127.0.0.1:8092
	BotURL           string  // 默认 http://127.0.0.1:8091
	InternalToken    string  // 与 bot 共享的内网令牌（可选，双方一致即启用校验）
	MergeThreshold   float64 // 相似度 >= 该值 → 归并到已有 Issue（默认 0.65）
	RelatedThreshold float64 // 相似度 >= 该值 → 新建但在回复里提示相似（默认 0.30）
}

func LoadFromEnv() Config {
	return Config{
		HTTPAddr:         getenv("ORCH_HTTP_ADDR", ":8090"),
		IssueServiceURL:  getenv("ISSUE_SERVICE_URL", "http://127.0.0.1:8092"),
		BotURL:           getenv("BOT_URL", "http://127.0.0.1:8091"),
		InternalToken:    os.Getenv("INTERNAL_TOKEN"),
		MergeThreshold:   getf("ORCH_MERGE_THRESHOLD", 0.65),
		RelatedThreshold: getf("ORCH_RELATED_THRESHOLD", 0.30),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getf(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return def
}
