package scheduler

import (
	"strings"
	"testing"
)

func TestBuildChatPromptIncludesReferencedIssueBody(t *testing.T) {
	p := &Project{Name: "Syntropy", Repo: "Ch3nYe/Syntropy", DefaultBase: "main"}
	msg := Inbound{GroupName: "Syntropy产品开发", SenderName: "陈国强", Text: "这个 Issue 里哪有图片？"}
	issue := &Issue{Number: 432, Title: "选区浮层增加引用按钮", Body: "现象：正文没有图片链接。"}
	out := buildChatPrompt(p, nil, msg, 432, nil, issue)
	for _, want := range []string{"Issue #432 标题", "Issue #432 正文", "正文没有图片链接", "正文没有图片链接就明确说没有"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}

func TestBuildChatPromptWithoutIssueBody(t *testing.T) {
	p := &Project{Name: "Syntropy", Repo: "Ch3nYe/Syntropy", DefaultBase: "main"}
	out := buildChatPrompt(p, nil, Inbound{Text: "hi"}, 0, nil, nil)
	if strings.Contains(out, "Issue #0") {
		t.Fatalf("unexpected issue section: %s", out)
	}
}
