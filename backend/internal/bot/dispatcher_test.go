package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)
func TestBridgeDispatcherPostsMessage(t *testing.T) {
	var got map[string]any
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bridge/messages" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-Bridge-Token") != "bt" {
			t.Fatalf("missing bridge token")
		}
		hits++
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer srv.Close()

	d := BridgeDispatcher{BaseURL: srv.URL, Token: "bt"}
	replied := false
	err := d.Dispatch(context.Background(), Inbound{
		Platform: "lanxin", PlatformMessageID: "m-1", ChatType: "group",
		SenderID: "u-1", GroupID: "g-1", Text: "启动后闪退\n更多细节",
		Reply: func(ctx context.Context, text string) error { replied = true; return nil },
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if hits != 1 {
		t.Fatalf("bridge hits = %d", hits)
	}
	if replied {
		t.Fatal("bridge 已经会发回执，bot 不应再回一条")
	}
	if got["message_id"] != "m-1" || got["chat_id"] != "g-1" || got["user_id"] != "u-1" {
		t.Fatalf("payload = %+v", got)
	}
	if got["title"] != "启动后闪退" {
		t.Fatalf("title should be first line, got %v", got["title"])
	}
	if got["body"] != "启动后闪退\n更多细节" {
		t.Fatalf("body = %v", got["body"])
	}
}