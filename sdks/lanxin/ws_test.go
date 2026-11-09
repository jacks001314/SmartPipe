package lanxin

import (
	"encoding/json"
	"testing"
)

// 群聊事件解析（取自官方文档 6A 节示例）。
func TestParseGroupEvent(t *testing.T) {
	raw := []byte(`{"events":[{"type":"bot_group_message","data":{
		"msgType":"text",
		"msgData":{"text":{"content":"你好"}},
		"chatType":"group",
		"from":"staff-1","senderName":"张三","fromType":0,
		"groupId":"g-1","groupName":"研发群",
		"conversationId":"c-1","messageId":"12","msgId":"m-1",
		"reminder":{"isAtMe":true,"isAtAll":false,"bots":[{"botId":"b1","botName":"pipe"}]}
	}}]}`)

	var env eventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Events) != 1 || env.Events[0].Type != EventGroupMessage {
		t.Fatalf("bad envelope: %+v", env)
	}
	msg, cb, err := ParseEvent(env.Events[0])
	if err != nil || msg == nil || cb != nil {
		t.Fatalf("parse: msg=%v cb=%v err=%v", msg, cb, err)
	}
	if msg.MsgID != "m-1" || msg.From != "staff-1" || msg.GroupID != "g-1" {
		t.Fatalf("fields: %+v", msg)
	}
	if msg.Reminder == nil || !msg.Reminder.IsAtMe {
		t.Fatalf("reminder: %+v", msg.Reminder)
	}
	if msg.TextContent() != "你好" {
		t.Fatalf("text: %q", msg.TextContent())
	}
}

// 入站富文本 msgType 为 format（出站为 formatText），text 提取要兼容两种键。
func TestParseFormatMessage(t *testing.T) {
	m := &InboundMessage{
		MsgType: "format",
		MsgData: json.RawMessage(`{"format":{"formatType":1,"text":"**hi**"}}`),
	}
	if m.TextContent() != "**hi**" {
		t.Fatalf("text: %q", m.TextContent())
	}
}

// approve_card_callback 解析。
func TestParseApproveCallback(t *testing.T) {
	ev := Event{Type: EventApproveCardCB, Data: json.RawMessage(`{"eventData":"once:req-1","staffId":"s-9"}`)}
	msg, cb, err := ParseEvent(ev)
	if err != nil || msg != nil || cb == nil {
		t.Fatalf("msg=%v cb=%v err=%v", msg, cb, err)
	}
	if cb.EventData != "once:req-1" || cb.StaffID != "s-9" {
		t.Fatalf("cb: %+v", cb)
	}
}

// 图片/文件/语音/视频消息的 mediaIds 在各自的 msgData 段里，不能只看 text。
func TestMediaAttachmentParsesMsgTypeSections(t *testing.T) {
	cases := []struct {
		name    string
		msgType string
		msgData string
		want    MediaType
		ids     []string
	}{
		{"text", "text", `{"text":{"content":"看下配置","mediaType":3,"mediaIds":["m-text"]}}`, MediaFile, []string{"m-text"}},
		{"image", "image", `{"image":{"content":"截图","mediaIds":["m-image"]}}`, MediaImage, []string{"m-image"}},
		{"file", "file", `{"file":{"content":"配置文件","name":"config.toml","mediaIds":["m-file"]}}`, MediaFile, []string{"m-file"}},
		{"voice", "voice", `{"voice":{"content":"语音","mediaIds":["m-voice"]}}`, MediaVoice, []string{"m-voice"}},
		{"video", "video", `{"video":{"content":"录屏","mediaIds":["m-video","m-cover"]}}`, MediaVideo, []string{"m-video", "m-cover"}},
		// 有些网关会把媒体挂在 text 段，同时 msgType 仍是 file；兼容回退。
		{"fallback", "file", `{"text":{"content":"","mediaType":3,"mediaIds":["m-fallback"]}}`, MediaFile, []string{"m-fallback"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &InboundMessage{MsgType: tc.msgType, MsgData: json.RawMessage(tc.msgData)}
			gotType, gotIDs := m.MediaAttachment()
			if gotType != tc.want {
				t.Fatalf("type = %d, want %d", gotType, tc.want)
			}
			if len(gotIDs) != len(tc.ids) {
				t.Fatalf("ids = %v, want %v", gotIDs, tc.ids)
			}
			for i := range tc.ids {
				if gotIDs[i] != tc.ids[i] {
					t.Fatalf("ids = %v, want %v", gotIDs, tc.ids)
				}
			}
		})
	}
}

// msgType 决定优先读取的段，避免其他段里的 mediaIds 抢走真正的附件。
func TestMediaAttachmentPrefersMsgTypeSection(t *testing.T) {
	m := &InboundMessage{
		MsgType: "file",
		MsgData: json.RawMessage(`{"text":{"mediaIds":["m-text"]},"file":{"mediaIds":["m-file"]}}`),
	}
	gotType, ids := m.MediaAttachment()
	if gotType != MediaFile || len(ids) != 1 || ids[0] != "m-file" {
		t.Fatalf("type=%d ids=%v", gotType, ids)
	}
}

func TestMediaAttachmentEmptyForPlainMessages(t *testing.T) {
	m := &InboundMessage{MsgType: "text", MsgData: json.RawMessage(`{"text":{"content":"你好"}}`)}
	if kind, ids := m.MediaAttachment(); kind != 0 || len(ids) != 0 {
		t.Fatalf("kind=%d ids=%v", kind, ids)
	}
	m = &InboundMessage{MsgType: "position", MsgData: json.RawMessage(`{"position":{"name":"公司"}}`)}
	if kind, ids := m.MediaAttachment(); kind != 0 || len(ids) != 0 {
		t.Fatalf("kind=%d ids=%v", kind, ids)
	}
}
