package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"smartpipe/sdks/lanxin"
)

// 普通文件附件即使机器人开启了崩溃上报，也必须交给调度器，而不是 CrashIntake。
func TestHandlerDispatchesFileAttachmentInsteadOfCrash(t *testing.T) {
	snd := &fakeSender{}
	disp := &recordingDispatcher{}
	media := &fakeMedia{data: []byte("title = \"smartpipe\"\nserver = \"localhost\"\n")}
	crashMedia := &fakeMedia{data: validBundle()}
	h := NewHandler(Config{RequireAtMe: true}, snd, disp).
		WithMedia(media).
		WithCrash(&CrashIntake{IngestURL: "http://127.0.0.1:1", Media: crashMedia})

	ev := lanxin.Event{Type: lanxin.EventGroupMessage, Data: json.RawMessage(`{
		"msgType":"file","chatType":"group","from":"u-1","msgId":"msg-file-1","groupId":"grp-1",
		"msgData":{"file":{"content":"文件内容是什么","name":"config.toml","mediaIds":["media-file-1"]}},
		"reminder":{"isAtMe":true}}`)}
	if err := h.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	if len(disp.got) != 1 {
		t.Fatalf("dispatch count = %d", len(disp.got))
	}
	got := disp.got[0]
	if !strings.Contains(got.Text, "文件内容是什么") {
		t.Fatalf("text = %q", got.Text)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	att := got.Attachments[0]
	if att.Name != "config.toml" || att.MimeType != "text/plain" {
		t.Fatalf("attachment = %+v", att)
	}
	if string(att.Data) != string(media.data) {
		t.Fatalf("attachment data = %q", att.Data)
	}
	if crashMedia.calls != 0 || len(snd.groups) != 0 {
		t.Fatalf("file must not go through crash intake: crash=%d replies=%d", crashMedia.calls, len(snd.groups))
	}
	if media.calls != 1 {
		t.Fatalf("media download calls = %d", media.calls)
	}
}

func TestAttachmentCaptionSupportsAllMediaSections(t *testing.T) {
	cases := []struct {
		name    string
		msgType string
		msgData string
		want    string
	}{
		{"image", "image", `{"image":{"content":"看这张图","mediaIds":["m"]}}`, "看这张图"},
		{"file", "file", `{"file":{"content":"看这个配置","mediaIds":["m"]}}`, "看这个配置"},
		{"voice", "voice", `{"voice":{"content":"听这段语音","mediaIds":["m"]}}`, "听这段语音"},
		{"video", "video", `{"video":{"content":"看这段录屏","mediaIds":["m"]}}`, "看这段录屏"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &lanxin.InboundMessage{MsgType: tc.msgType, MsgData: json.RawMessage(tc.msgData)}
			if got := attachmentCaption(m); got != tc.want {
				t.Fatalf("caption = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAttachmentNameFallsBackToFileNameAndMime(t *testing.T) {
	named := &lanxin.InboundMessage{MsgType: "file", MsgData: json.RawMessage(`{"file":{"fileName":"notes.md","mediaIds":["m"]}}`)}
	if got := attachmentName(named, 0, "text/markdown"); got != "notes.md" {
		t.Fatalf("name = %q", got)
	}
	unnamed := &lanxin.InboundMessage{MsgType: "file", MsgData: json.RawMessage(`{"file":{"mediaIds":["m"]}}`)}
	if got := attachmentName(unnamed, 0, "text/plain; charset=utf-8"); got != "attachment-1.txt" {
		t.Fatalf("name = %q", got)
	}
}

func TestIsCrashBundleUsesFileNameFallback(t *testing.T) {
	m := &lanxin.InboundMessage{MsgType: "file", MsgData: json.RawMessage(`{"file":{"fileName":"crash_bundle.json","mediaIds":["m"]}}`)}
	if !isCrashBundle(m, "") {
		t.Fatal("crash_bundle.json via fileName must be detected")
	}
}
