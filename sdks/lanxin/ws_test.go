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
