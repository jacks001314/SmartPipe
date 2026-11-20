package scheduler

import (
	"context"
	"strings"
	"testing"
)

func TestBuildChatPromptIncludesParticipantsAndSender(t *testing.T) {
	p := &Project{Name: "Syntropy", Repo: "Ch3nYe/Syntropy", DefaultBase: "main"}
	msg := Inbound{ChatType: "group", GroupName: "研发群", SenderID: "u1", SenderName: "张三", Text: "让李四确认下"}
	history := []ChatMessage{{Role: "user", SenderName: "李四", Body: "这个导出有点问题"}}
	out := buildChatPrompt(p, history, []Participant{{ID: "u1", Name: "张三"}, {ID: "u2", Name: "李四"}}, msg, 0, nil, nil)
	for _, want := range []string{"群成员", "张三 (id=u1)", "李四 (id=u2)", "李四：这个导出有点问题", "mention"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}

func TestResolveMentionOnlyAcceptsKnownParticipants(t *testing.T) {
	msg := Inbound{ChatType: "group", SenderID: "u1", SenderName: "张三"}
	participants := []Participant{{ID: "u1", Name: "张三"}, {ID: "u2", Name: "李四"}}

	if id, name := resolveMention("李四", participants, msg); id != "u2" || name != "李四" {
		t.Fatalf("valid mention resolved to %q/%q", id, name)
	}
	if id, name := resolveMention("@李四", participants, msg); id != "u2" || name != "李四" {
		t.Fatalf("@ prefixed mention resolved to %q/%q", id, name)
	}
	if id, name := resolveMention("王五", participants, msg); id != "u1" || name != "张三" {
		t.Fatalf("unknown mention should fallback to sender, got %q/%q", id, name)
	}
	if id, name := resolveMention("李四", participants, Inbound{ChatType: "p2p", SenderID: "u1"}); id != "" || name != "" {
		t.Fatalf("p2p should not mention, got %q/%q", id, name)
	}
}

func TestChatAnswerMentionsSelectedParticipant(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.repo.RecordMessage(context.Background(), MessageRecord{
		PlatformMessageID: "old-1", ChatType: "group", ChatID: "group-1",
		SenderID: "u2", SenderName: "李四", Text: "之前说过导出问题",
	}); err != nil {
		t.Fatal(err)
	}
	h.runner.chatReply = `{"action":"answer","reply":"李四你看看这个","mention":"李四"}`
	if err := h.s.HandleMessage(context.Background(), msg("m-mention", "这个让李四看下")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := h.notifier.allMentions()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].ID != "u2" || got[0][0].Name != "李四" {
		t.Fatalf("mentions = %#v", got)
	}
}

func TestChatAnswerDefaultsToSenderMention(t *testing.T) {
	h := newHarness(t, Config{})
	h.runner.chatReply = `{"action":"answer","reply":"这个功能在设置页开启。"}`
	if err := h.s.HandleMessage(context.Background(), msg("m-default-mention", "怎么开启自动同步？")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := h.notifier.allMentions()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].ID != "u1" || got[0][0].Name != "张三" {
		t.Fatalf("mentions = %#v", got)
	}
}

func TestSmartAnswerUsesTriageMention(t *testing.T) {
	h := newHarness(t, Config{})
	h.project.Policy = ChatPolicy{Mode: "smart", AnswerConfidence: 0.5}
	if err := h.repo.RecordMessage(context.Background(), MessageRecord{
		PlatformMessageID: "old-2", ChatType: "group", ChatID: "group-1",
		SenderID: "u2", SenderName: "李四", Text: "导出少行是已知问题",
	}); err != nil {
		t.Fatal(err)
	}
	h.runner.triageReply = `{"action":"answer","reply":"李四这个问题已经修了","mention":"李四","confidence":0.9,"reason":"指向项目"}`
	if err := h.s.HandleMessage(context.Background(), nonAtMsg("m-triage-mention", "之前那个导出问题修了吗")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := h.notifier.allMentions()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].ID != "u2" {
		t.Fatalf("mentions = %#v", got)
	}
}
