package bot

import (
	"context"
	"encoding/json"
	"testing"

	"smartpipe/sdks/lanxin"
)

type fakeDirectory struct {
	info        *lanxin.GroupInfo
	members     []lanxin.GroupMember
	infoCalls   int
	memberCalls int
}

func (f *fakeDirectory) GroupInfo(ctx context.Context, groupID string) (*lanxin.GroupInfo, error) {
	f.infoCalls++
	return f.info, nil
}

func (f *fakeDirectory) GroupMembers(ctx context.Context, groupID string, offset, limit int) ([]lanxin.GroupMember, error) {
	f.memberCalls++
	return f.members, nil
}

func groupEventWithoutNames(msgID string) lanxin.Event {
	raw, _ := json.Marshal(map[string]any{
		"msgType":  "text",
		"msgData":  map[string]any{"text": map[string]any{"content": "前端修改"}},
		"chatType": "group",
		"from":     "staff-1",
		"groupId":  "g-1",
		"msgId":    msgID,
		"reminder": map[string]any{"isAtMe": true},
	})
	return lanxin.Event{Type: lanxin.EventGroupMessage, Data: raw}
}

func TestGroupMessageResolvesRealNames(t *testing.T) {
	snd := &fakeSender{}
	disp := &recordingDispatcher{}
	dir := &fakeDirectory{
		info:    &lanxin.GroupInfo{Name: "Syntropy产品开发"},
		members: []lanxin.GroupMember{{StaffID: "staff-1", Name: "陈国强"}},
	}
	h := NewHandler(Config{RequireAtMe: true}, snd, disp).WithDirectory(dir)

	if err := h.HandleEvent(context.Background(), groupEventWithoutNames("m-name-1")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(disp.got) != 1 {
		t.Fatalf("dispatch count = %d", len(disp.got))
	}
	got := disp.got[0]
	if got.GroupName != "Syntropy产品开发" || got.SenderName != "陈国强" {
		t.Fatalf("resolved names = %q / %q", got.GroupName, got.SenderName)
	}

	if err := h.HandleEvent(context.Background(), groupEventWithoutNames("m-name-2")); err != nil {
		t.Fatalf("handle second: %v", err)
	}
	if dir.infoCalls != 1 || dir.memberCalls != 1 {
		t.Fatalf("directory should be cached: info=%d members=%d", dir.infoCalls, dir.memberCalls)
	}
}
