package bot

import (
	"context"
	"testing"
)

type fakeInbox struct {
	reports []InboxReport
}

func (f *fakeInbox) Report(ctx context.Context, r InboxReport) error {
	f.reports = append(f.reports, r)
	return nil
}

func TestFilteredGroupMessageReportedToInbox(t *testing.T) {
	inbox := &fakeInbox{}
	h := NewHandler(Config{RequireAtMe: true}, &fakeSender{}, AckDispatcher{}).WithInbox(inbox)

	if err := h.HandleEvent(context.Background(), groupEvent("m-filtered", "没 @ 机器人", false)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(inbox.reports) != 1 {
		t.Fatalf("reports = %d", len(inbox.reports))
	}
	got := inbox.reports[0]
	if got.MessageID != "m-filtered" || got.Reason != "群消息未 @ 机器人" || got.ChatID != "g-1" {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestActionableMessageNotReportedAsFiltered(t *testing.T) {
	inbox := &fakeInbox{}
	fs := &fakeSender{}
	h := NewHandler(Config{RequireAtMe: true}, fs, AckDispatcher{}).WithInbox(inbox)

	if err := h.HandleEvent(context.Background(), groupEvent("m-action", "@机器人 报个bug", true)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(inbox.reports) != 0 {
		t.Fatalf("actionable message should be recorded by bridge, got %+v", inbox.reports)
	}
}
