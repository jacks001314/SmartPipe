package bot

import (
	"context"
	"testing"
)

func TestRuntimeRoutesReplyByBotID(t *testing.T) {
	rt := NewRuntime()
	s1 := &fakeSender{}
	s2 := &fakeSender{}
	rt.Add("bot-1", NewHandler(Config{BotID: "bot-1", BotName: "一号"}, s1, AckDispatcher{}))
	rt.Add("bot-2", NewHandler(Config{BotID: "bot-2", BotName: "二号"}, s2, AckDispatcher{}))
	if err := rt.SendText(context.Background(), "bot-2", "group", "grp-2", "", "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(s1.group) != 0 || len(s2.group) != 1 || s2.group[0] != "grp-2" {
		t.Fatalf("wrong bot routed: bot1=%v bot2=%v", s1.group, s2.group)
	}
	if err := rt.SendText(context.Background(), "missing", "group", "grp-x", "", "hello"); err == nil {
		t.Fatal("expected missing bot error")
	}
}
