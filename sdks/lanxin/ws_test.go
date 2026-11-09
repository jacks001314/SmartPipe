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

// 图片/文件/语音/视频的 mediaIds 在各自的 msgData 段里，必须按 msgType 取对应段。
func TestMediaAttachmentByMsgType(t *testing.T) {
	cases := []struct {
		name    string
		msgType string
		msgData string
		want    MediaType
		ids     []string
	}{
		{"file", "file", `{"file":{"mediaIds":["file-1"],"content":"看配置"}}`, MediaFile, []string{"file-1"}},
		{"image", "image", `{"image":{"mediaIds":["img-1"],"content":"看截图"}}`, MediaImage, []string{"img-1"}},
		{"voice", "voice", `{"voice":{"mediaIds":["voice-1"],"content":"听语音"}}`, MediaVoice, []string{"voice-1"}},
		{"video", "video", `{"video":{"mediaIds":["video-1","cover-1"],"content":"看录屏"}}`, MediaVideo, []string{"video-1", "cover-1"}},
		{"text-media", "text", `{"text":{"content":"附件","mediaType":3,"mediaIds":["txt-1"]}}`, MediaFile, []string{"txt-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &InboundMessage{MsgType: tc.msgType, MsgData: json.RawMessage(tc.msgData)}
			kind, ids := m.MediaAttachment()
			if kind != tc.want {
				t.Fatalf("media type = %d, want %d", kind, tc.want)
			}
			if len(ids) != len(tc.ids) {
				t.Fatalf("ids = %v, want %v", ids, tc.ids)
			}
			for i := range ids {
				if ids[i] != tc.ids[i] {
					t.Fatalf("ids = %v, want %v", ids, tc.ids)
				}
			}
		})
	}
}

// msgType=file 时即使 text 段也带了 mediaIds，也应优先用 file 段。
func TestMediaAttachmentPrefersSectionForMsgType(t *testing.T) {
	m := &InboundMessage{MsgType: "file", MsgData: json.RawMessage(`{
		"text":{"mediaType":2,"mediaIds":["unrelated-image"]},
		"file":{"mediaIds":["real-file"]}}`)}
	kind, ids := m.MediaAttachment()
	if kind != MediaFile || len(ids) != 1 || ids[0] != "real-file" {
		t.Fatalf("kind=%d ids=%v", kind, ids)
	}
}

// 兼容 msgType 与段名不一致的报文：仍能从 image 段取到媒体。
func TestMediaAttachmentFallsBackToAnySection(t *testing.T) {
	m := &InboundMessage{MsgType: "unknown", MsgData: json.RawMessage(`{"image":{"mediaIds":["img-2"]}}`)}
	kind, ids := m.MediaAttachment()
	if kind != MediaImage || len(ids) != 1 || ids[0] != "img-2" {
		t.Fatalf("kind=%d ids=%v", kind, ids)
	}
}
