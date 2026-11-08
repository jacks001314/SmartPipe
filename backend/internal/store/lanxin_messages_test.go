package store

import (
	"context"
	"testing"
)

func TestLanxinInboxLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.DB().Exec("DELETE FROM lanxin_messages")

	rec, err := s.RecordLanxinInbound(ctx, LanxinMessage{
		PlatformMessageID: "m-1", ChatType: "group", ChatID: "g-1", UserID: "u-1",
		SenderName: "张三", MsgType: "text", Text: "登录闪退", Status: LanxinStatusReceived,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if rec.Status != LanxinStatusReceived || rec.Text != "登录闪退" {
		t.Fatalf("unexpected record: %+v", rec)
	}

	if err := s.MarkLanxinMessage(ctx, "m-1", LanxinStatusIssueCreated, "", 12, "https://example.test/12"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	// 同一消息重复上报（例如 bot 重连）不能把已建单状态降级。
	again, err := s.RecordLanxinInbound(ctx, LanxinMessage{
		PlatformMessageID: "m-1", ChatType: "group", ChatID: "g-1", UserID: "u-1",
		MsgType: "text", Text: "登录闪退（重推）", Status: LanxinStatusReceived,
	})
	if err != nil {
		t.Fatalf("record again: %v", err)
	}
	if again.Status != LanxinStatusIssueCreated {
		t.Fatalf("status downgraded: %s", again.Status)
	}
	if again.Text != "登录闪退（重推）" {
		t.Fatalf("text not refreshed: %q", again.Text)
	}

	// 只走了 /bridge/messages 的旧版 bot 也要能补出一条记录。
	if err := s.MarkLanxinMessage(ctx, "m-2", LanxinStatusUnbound, "群未绑定项目", 0, ""); err != nil {
		t.Fatalf("mark missing: %v", err)
	}

	list, err := s.ListLanxinMessages(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d", len(list))
	}
	found := false
	for _, item := range list {
		if item.PlatformMessageID == "m-1" {
			found = true
			if item.Status != LanxinStatusIssueCreated || item.IssueNumber != 12 || item.IssueURL == "" {
				t.Fatalf("unexpected m-1: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("m-1 missing from list: %+v", list)
	}
}
