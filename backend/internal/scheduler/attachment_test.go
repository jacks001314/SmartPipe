package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 附件必须落盘：普通文件给模型路径，图片同时作为图片输入。
func TestSaveChatAttachmentsWritesFilesAndImages(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000")
	msg := Inbound{Attachments: []Attachment{
		{Name: "config.toml", MimeType: "text/plain; charset=utf-8", Data: []byte("title = \"smartpipe\"\n")},
		{Name: "error.png", MimeType: "image/png", Data: png},
	}}
	images, listing, cleanup, err := saveChatAttachments(msg)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(images) != 1 || filepath.Base(images[0]) != "error.png" {
		t.Fatalf("images = %v", images)
	}
	if len(listing) != 2 || !strings.Contains(strings.Join(listing, "\n"), "config.toml") {
		t.Fatalf("listing = %v", listing)
	}
	gotPNG, err := os.ReadFile(images[0])
	if err != nil || string(gotPNG) != string(png) {
		t.Fatalf("image on disk = %q err=%v", gotPNG, err)
	}

	// 普通文件的 listing 里应带可读取的绝对路径。
	var filePath string
	for _, line := range listing {
		if strings.Contains(line, "config.toml") {
			parts := strings.Split(line, "：")
			filePath = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if filePath == "" || !filepath.IsAbs(filePath) {
		t.Fatalf("file path missing from listing: %v", listing)
	}
	data, err := os.ReadFile(filePath)
	if err != nil || string(data) != "title = \"smartpipe\"\n" {
		t.Fatalf("file on disk = %q err=%v", data, err)
	}

	dir := filepath.Dir(filePath)
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left dir %s: %v", dir, err)
	}
}

func TestSaveChatAttachmentsEmptyIsNoop(t *testing.T) {
	images, listing, cleanup, err := saveChatAttachments(Inbound{})
	if err != nil || len(images) != 0 || len(listing) != 0 {
		t.Fatalf("images=%v listing=%v err=%v", images, listing, err)
	}
	cleanup()
}

// 短信链路：附件随消息进入 chat worker，普通文件路径要出现在提示词里。
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

// 只有附件、没有文字的消息也要能进入模型。
func TestChatAcceptsAttachmentOnlyMessage(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"这个文件定义了项目地址。"}`
	m := msg("m-attach-only", "")
	m.Attachments = []Attachment{
		{Name: "config.toml", MimeType: "text/plain", Data: []byte("repo = \"acme/core\"\n")},
	}
	if err := h.s.HandleMessage(context.Background(), m); err != nil {
		t.Fatalf("handle: %v", err)
	}
	spec := h.runner.last()
	if !strings.Contains(spec.Prompt, "只带了附件") || !strings.Contains(spec.Prompt, "config.toml") {
		t.Fatalf("prompt = %s", spec.Prompt)
	}
}
