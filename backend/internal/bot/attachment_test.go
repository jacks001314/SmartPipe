package bot

import (
	"context"
	"encoding/json"
	"testing"

	"smartpipe/sdks/lanxin"
)

// file 消息的 mediaIds 在 msgData.file 段：解析后要下载并交给调度器，
// 不能再走“暂不处理的消息类型”，也不能被崩溃上报截走。
func TestHandlerDispatchesFileMessageToModel(t *testing.T) {
	snd := &fakeSender{}
	disp := &recordingDispatcher{}
	media := &fakeMedia{data: []byte("port = 8080\n")}
	crashMedia := &fakeMedia{data: validBundle()}
	h := NewHandler(Config{RequireAtMe: true}, snd, disp).
		WithCrash(&CrashIntake{IngestURL: "http://127.0.0.1:1", Media: crashMedia}).
		WithMedia(media)

	ev := lanxin.Event{Type: lanxin.EventGroupMessage, Data: json.RawMessage(`{
		"msgType":"file","chatType":"group","from":"u-1","senderName":"张三","msgId":"msg-file-1","groupId":"grp-1",
		"msgData":{"file":{"name":"config.toml","mediaIds":["media-1"],"content":"文件内容是什么"}},
		"reminder":{"isAtMe":true}}`)}
	if err := h.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(disp.got) != 1 {
		t.Fatalf("dispatch count = %d", len(disp.got))
	}
	got := disp.got[0]
	if got.Text != "文件内容是什么" {
		t.Fatalf("text = %q", got.Text)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	att := got.Attachments[0]
	if att.Name != "config.toml" || att.MimeType != "text/plain" || string(att.Data) != "port = 8080\n" {
		t.Fatalf("attachment = %+v", att)
	}
	if crashMedia.calls != 0 || len(snd.groups) != 0 {
		t.Fatalf("non-crash attachment must not hit crash intake: calls=%d replies=%d", crashMedia.calls, len(snd.groups))
	}
	if media.calls != 1 {
		t.Fatalf("media download calls = %d", media.calls)
	}
}

// 只有附件、没有文字说明的图片消息同样要派单给模型。
func TestHandlerDispatchesImageWithoutCaption(t *testing.T) {
	disp := &recordingDispatcher{}
	h := NewHandler(Config{}, &fakeSender{}, disp).
		WithMedia(&fakeMedia{data: []byte("\x89PNG\r\n\x1a\n0000")})
	ev := lanxin.Event{Type: lanxin.EventP2PMessage, Data: json.RawMessage(`{
		"msgType":"image","chatType":"p2p","from":"u-1","msgId":"msg-img-2",
		"msgData":{"image":{"mediaIds":["media-1"]}}}`)}
	if err := h.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(disp.got) != 1 || len(disp.got[0].Attachments) != 1 || disp.got[0].Text != "" {
		t.Fatalf("dispatch = %+v", disp.got)
	}
	att := disp.got[0].Attachments[0]
	if att.MimeType != "image/png" || att.Name != "attachment-1.png" {
		t.Fatalf("attachment = %+v", att)
	}
}

// crash_bundle 即使以 file 消息形式发来，也必须走一键上报而不是模型。
func TestHandlerKeepsFileCrashBundleOffModel(t *testing.T) {
	snd := &fakeSender{}
	disp := &recordingDispatcher{}
	crashMedia := &fakeMedia{data: validBundle()}
	h := NewHandler(Config{}, snd, disp).
		WithCrash(&CrashIntake{IngestURL: "http://127.0.0.1:1", Media: crashMedia}).
		WithMedia(&fakeMedia{data: validBundle()})
	ev := lanxin.Event{Type: lanxin.EventP2PMessage, Data: json.RawMessage(`{
		"msgType":"file","chatType":"p2p","from":"u-1","msgId":"msg-crash-file",
		"msgData":{"file":{"mediaIds":["media-1"],"fileName":"crash_bundle.json"}}}`)}
	// 上报地址不可达，这里只关心它没有落到模型侧（错误由 handler 返回）。
	_ = h.HandleEvent(context.Background(), ev)
	if len(disp.got) != 0 {
		t.Fatalf("crash bundle must not be dispatched to model: %+v", disp.got)
	}
	if crashMedia.calls != 1 || len(snd.p2ps) != 1 {
		t.Fatalf("crash intake calls=%d replies=%d", crashMedia.calls, len(snd.p2ps))
	}
}

// 语音/视频段自带的 content 也要作为附件说明交给模型。
func TestAttachmentCaptionSupportsVoiceAndVideo(t *testing.T) {
	for _, tc := range []struct{ msgType, msgData, want string }{
		{"voice", `{"voice":{"content":"听语音","mediaIds":["m"]}}`, "听语音"},
		{"video", `{"video":{"content":"看录屏","mediaIds":["m"]}}`, "看录屏"},
	} {
		m := &lanxin.InboundMessage{MsgType: tc.msgType, MsgData: json.RawMessage(tc.msgData)}
		if got := attachmentCaption(m); got != tc.want {
			t.Fatalf("%s caption = %q", tc.msgType, got)
		}
	}
}
