package orchestrator

import (
	"context"
	"strings"
	"testing"

	"smartpipe/backend/internal/bot"
)

type stubTriage struct {
	res TriageResult
	err error
}

func (s *stubTriage) Triage(context.Context, bot.Inbound) (TriageResult, error) {
	return s.res, s.err
}

type fakeBot struct {
	chat, target, ref, text string
}

func (f *fakeBot) Reply(_ context.Context, chatType, targetID, refMsgID, text string) error {
	f.chat, f.target, f.ref, f.text = chatType, targetID, refMsgID, text
	return nil
}

func TestEngineRepliesCreated(t *testing.T) {
	fb := &fakeBot{}
	o := &Orchestrator{
		Tri: &stubTriage{res: TriageResult{Action: "created", IssueID: "ISS-T-0001", Title: "登录失败"}},
		Bot: fb,
	}
	if err := o.HandleInbound(context.Background(), sampleMsg("登录失败")); err != nil {
		t.Fatal(err)
	}
	if fb.chat != "group" || fb.target != "g-1" || fb.ref != "m-1" {
		t.Fatalf("reply meta: %+v", fb)
	}
	for _, want := range []string{"ISS-T-0001", "登录失败", "每日 06:00"} {
		if !strings.Contains(fb.text, want) {
			t.Fatalf("reply missing %q: %s", want, fb.text)
		}
	}
}

func TestEngineRepliesMerged(t *testing.T) {
	fb := &fakeBot{}
	o := &Orchestrator{
		Tri: &stubTriage{res: TriageResult{Action: "merged", IssueID: "ISS-T-0009", Title: "登录失败"}},
		Bot: fb,
	}
	if err := o.HandleInbound(context.Background(), sampleMsg("登录失败")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fb.text, "合并") || !strings.Contains(fb.text, "ISS-T-0009") {
		t.Fatalf("merged reply: %s", fb.text)
	}
}

func TestP2PTargetIsSender(t *testing.T) {
	fb := &fakeBot{}
	o := &Orchestrator{
		Tri: &stubTriage{res: TriageResult{Action: "created", IssueID: "ISS-T-0002", Title: "x"}},
		Bot: fb,
	}
	msg := sampleMsg("私聊报障")
	msg.ChatType = "p2p"
	msg.GroupID = ""
	if err := o.HandleInbound(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if fb.target != "s-1" {
		t.Fatalf("p2p target=%s", fb.target)
	}
}
