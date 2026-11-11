package bot

import (
	"context"
	"encoding/json"
	"testing"

	"smartpipe/sdks/lanxin"
)

type fakeSender struct {
	p2p    []string
	group  []string
	groups []lanxin.Message
	p2ps   []lanxin.Message
	failMD bool
}

func (f *fakeSender) SendP2PMessage(ctx context.Context, userID string, msg *lanxin.Message) (json.RawMessage, error) {
	f.p2p = append(f.p2p, userID)
	f.p2ps = append(f.p2ps, *msg)
	if f.failMD && msg.MsgType == lanxin.MsgTypeFormatText {
		return nil, context.Canceled
	}
	return json.RawMessage("{}"), nil
}

func (f *fakeSender) SendGroupMessage(ctx context.Context, groupID string, msg *lanxin.Message) (json.RawMessage, error) {
	f.group = append(f.group, groupID)
	f.groups = append(f.groups, *msg)
	if f.failMD && msg.MsgType == lanxin.MsgTypeFormatText {
		return nil, context.Canceled
	}
	return json.RawMessage("{}"), nil
}

func groupEvent(msgID, content string, atMe bool) lanxin.Event {
	data := map[string]any{
		"msgType":    "text",
		"msgData":    map[string]any{"text": map[string]any{"content": content}},
		"chatType":   "group",
		"from":       "staff-1",
		"senderName": "张三",
		"groupId":    "g-1",
		"msgId":      msgID,
	}
	if atMe {
		data["reminder"] = map[string]any{"isAtMe": true}
	}
	raw, _ := json.Marshal(data)
	return lanxin.Event{Type: lanxin.EventGroupMessage, Data: raw}
}

func p2pEvent(msgID, content string) lanxin.Event {
	raw, _ := json.Marshal(map[string]any{
		"msgType":    "text",
		"msgData":    map[string]any{"text": map[string]any{"content": content}},
		"chatType":   "p2p",
		"from":       "staff-9",
		"senderName": "李四",
		"msgId":      msgID,
	})
	return lanxin.Event{Type: lanxin.EventP2PMessage, Data: raw}
}

func TestGroupMessageRequiresAtMe(t *testing.T) {
	fs := &fakeSender{}
	h := NewHandler(Config{RequireAtMe: true}, fs, AckDispatcher{})

	if err := h.HandleEvent(context.Background(), groupEvent("m1", "报个bug", false)); err != nil {
		t.Fatal(err)
	}
	if len(fs.group) != 0 {
		t.Fatalf("未@消息不应回复, got %d", len(fs.group))
	}
	if err := h.HandleEvent(context.Background(), groupEvent("m2", "报个bug", true)); err != nil {
		t.Fatal(err)
	}
	if len(fs.group) != 1 || fs.group[0] != "g-1" {
		t.Fatalf("expect 1 reply to g-1, got %v", fs.group)
	}
}

func TestDedupByMsgID(t *testing.T) {
	fs := &fakeSender{}
	h := NewHandler(Config{}, fs, AckDispatcher{})

	ev := groupEvent("m-dup", "@bot 帮我看看", true)
	_ = h.HandleEvent(context.Background(), ev)
	_ = h.HandleEvent(context.Background(), ev)
	if len(fs.group) != 1 {
		t.Fatalf("msgId 幂等失败, replies=%d", len(fs.group))
	}
}

func TestP2PDispatched(t *testing.T) {
	fs := &fakeSender{}
	h := NewHandler(Config{}, fs, AckDispatcher{})

	if err := h.HandleEvent(context.Background(), p2pEvent("p1", "崩溃了")); err != nil {
		t.Fatal(err)
	}
	if len(fs.p2p) != 1 || fs.p2p[0] != "staff-9" {
		t.Fatalf("p2p replies: %v", fs.p2p)
	}
}

func TestNewCommandResets(t *testing.T) {
	fs := &fakeSender{}
	h := NewHandler(Config{RequireAtMe: false}, fs, AckDispatcher{})

	if err := h.HandleEvent(context.Background(), p2pEvent("n1", "/new")); err != nil {
		t.Fatal(err)
	}
	if len(fs.p2p) != 1 {
		t.Fatalf("/new 应直接回复确认, got %d", len(fs.p2p))
	}
}

func TestFormatTextFallback(t *testing.T) {
	fs := &fakeSender{failMD: true}
	h := NewHandler(Config{RequireAtMe: false}, fs, AckDispatcher{})

	if err := h.HandleEvent(context.Background(), p2pEvent("f1", "测试")); err != nil {
		t.Fatal(err)
	}
	if len(fs.p2ps) != 2 {
		t.Fatalf("markdown 失败应回退 text, sends=%d", len(fs.p2ps))
	}
	if fs.p2ps[0].MsgType != lanxin.MsgTypeFormatText || fs.p2ps[1].MsgType != lanxin.MsgTypeText {
		t.Fatalf("回退顺序错误: %s -> %s", fs.p2ps[0].MsgType, fs.p2ps[1].MsgType)
	}
}

func TestGroupNonAtMessagesForwardedWhenSchedulerConfigured(t *testing.T) {
	disp := &recordingDispatcher{}
	h := NewHandler(Config{RequireAtMe: true, SchedulerURL: "http://scheduler.local"}, &fakeSender{}, disp)
	if err := h.HandleEvent(context.Background(), groupEvent("m-observe", "导出偶发报错", false)); err != nil {
		t.Fatal(err)
	}
	if len(disp.got) != 1 || disp.got[0].IsAtMe || disp.got[0].ReplyToBot {
		t.Fatalf("non-at message should be observed: %+v", disp.got)
	}

	disp.got = nil
	if err := h.HandleEvent(context.Background(), groupEvent("m-command", "/status #12", false)); err != nil {
		t.Fatal(err)
	}
	if len(disp.got) != 1 {
		t.Fatalf("explicit command should be forwarded: %+v", disp.got)
	}

	disp.got = nil
	raw, _ := json.Marshal(map[string]any{
		"msgType": "text", "chatType": "group", "from": "staff-1", "senderName": "张三",
		"groupId": "g-1", "msgId": "m-reply-bot",
		"msgData":      map[string]any{"text": map[string]any{"content": "这个怎么处理"}},
		"referenceMsg": map[string]any{"from": "bot", "fromType": 1, "msgId": "bot-msg"},
	})
	if err := h.HandleEvent(context.Background(), lanxin.Event{Type: lanxin.EventGroupMessage, Data: raw}); err != nil {
		t.Fatal(err)
	}
	if len(disp.got) != 1 || !disp.got[0].ReplyToBot {
		t.Fatalf("reply-to-bot should be prioritized: %+v", disp.got)
	}
}
