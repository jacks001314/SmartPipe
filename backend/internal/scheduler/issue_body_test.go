package scheduler

import (
	"strings"
	"testing"
)

func TestBuildIssueBodyUsesNamesAndHidesIDs(t *testing.T) {
	msg := Inbound{
		BotName: "XTianTian", GroupName: "Syntropy产品开发", GroupID: "g-1",
		SenderName: "陈国强", SenderID: "staff-1", PlatformMessageID: "msg-1",
		Text: "@XTianTian #前端修改\nheader 的按钮和菜单字体一样大",
	}
	body := buildIssueBody(msg, &ChatDecision{Body: "需要调整 header 字体层级。"})
	for _, want := range []string{
		"- 群：Syntropy产品开发",
		"- 发言人：陈国强",
		"<!-- 蓝信元数据：会话ID=g-1；发言人ID=staff-1；消息ID=msg-1 -->",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("issue body missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"- 群：Syntropy产品开发（", "- 发言人：陈国强（"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("issue body should not expose IDs inline: %q", unwanted)
		}
	}
}

func TestBuildIssueBodyFallsBackWhenNameMissing(t *testing.T) {
	body := buildIssueBody(Inbound{Text: "hello"}, &ChatDecision{Body: "body"})
	if !strings.Contains(body, "- 群：未知群") || !strings.Contains(body, "- 发言人：未知成员") {
		t.Fatalf("fallback names missing:\n%s", body)
	}
}
