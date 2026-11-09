package scheduler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Content-Type 带 charset 时也要能识别图片并补正确扩展名。
func TestSaveChatAttachmentsNormalizesMimeType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000")
	images, listing, cleanup, err := saveChatAttachments(Inbound{Attachments: []Attachment{
		{Name: "screenshot", MimeType: "image/png; charset=binary", Data: png},
		{Name: "notes", MimeType: "text/plain; charset=utf-8", Data: []byte("hello")},
	}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	defer cleanup()
	if len(images) != 1 || filepath.Base(images[0]) != "screenshot.png" {
		t.Fatalf("images = %v", images)
	}
	joined := strings.Join(listing, "\n")
	if !strings.Contains(joined, "notes.txt") {
		t.Fatalf("listing = %v", listing)
	}
}

// 聊天链路：file 消息的附件路径要出现在提示词里，文件内容可读。
func TestChatInjectsFileAttachmentPathIntoPrompt(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"配置里只有标题和服务器地址。"}`
	m := msg("m-attach-file", "文件内容是什么")
	m.Attachments = []Attachment{
		{Name: "config.toml", MimeType: "text/plain; charset=utf-8", Data: []byte("title = \"smartpipe\"\n")},
	}
	if err := h.s.HandleMessage(context.Background(), m); err != nil {
		t.Fatalf("handle: %v", err)
	}
	spec := h.runner.last()
	if spec.Sandbox != "read-only" {
		t.Fatalf("chat worker sandbox = %q", spec.Sandbox)
	}
	if !strings.Contains(spec.Prompt, "config.toml") || !strings.Contains(spec.Prompt, "smartpipe-chat-attach-") {
		t.Fatalf("attachment missing from prompt: %s", spec.Prompt)
	}
	if !strings.Contains(spec.Prompt, "文件内容是什么") {
		t.Fatalf("message text missing from prompt: %s", spec.Prompt)
	}
	if len(spec.Images) != 0 {
		t.Fatalf("text file must not be sent as image: %v", spec.Images)
	}
}

// 图片附件必须作为图片输入交给模型，而不是只给一个路径。
func TestChatPassesImageAttachmentAsImageInput(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"截图里是登录超时。"}`
	m := msg("m-attach-image", "这个报错怎么处理")
	m.Attachments = []Attachment{
		{Name: "error.png", MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n0000")},
	}
	if err := h.s.HandleMessage(context.Background(), m); err != nil {
		t.Fatalf("handle: %v", err)
	}
	spec := h.runner.last()
	if len(spec.Images) != 1 || filepath.Base(spec.Images[0]) != "error.png" {
		t.Fatalf("images = %v", spec.Images)
	}
	if !strings.Contains(spec.Prompt, "图片 error.png") {
		t.Fatalf("image missing from prompt: %s", spec.Prompt)
	}
}
