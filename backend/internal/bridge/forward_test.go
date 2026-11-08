package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookForwardsIssueCommentToScheduler(t *testing.T) {
	got := make(chan schedulerEvent, 1)
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/scheduler/github" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		token = r.Header.Get("X-Scheduler-Token")
		var ev schedulerEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decode: %v", err)
		}
		got <- ev
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	h := &WebhookHandler{Secret: "s3cret", ForwardURL: srv.URL, ForwardToken: "tok"}
	body := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"acme/core"},
	  "issue":{"number":42,"title":"登录超时","state":"open","html_url":"https://github.com/acme/core/issues/42"},
	  "comment":{"id":99,"body":"请补一条测试","author_association":"MEMBER","user":{"login":"lisi"}}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-Hub-Signature-256", Sign("s3cret", body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	select {
	case ev := <-got:
		if ev.Event != "issue_comment" || ev.Action != "created" || ev.Issue != 42 {
			t.Fatalf("event=%+v", ev)
		}
		if ev.Author != "lisi" || !strings.Contains(ev.Body, "补一条测试") {
			t.Fatalf("comment not forwarded: %+v", ev)
		}
		if token != "tok" {
			t.Fatalf("token=%q", token)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event not forwarded to scheduler")
	}
}

func TestResolveProjectIDMatchesRepoURL(t *testing.T) {
	if !repoMatches("https://github.com/Acme/Core.git", "acme/core") {
		t.Fatal("https url should match")
	}
	if !repoMatches("acme/core", "acme/core") {
		t.Fatal("owner/name should match")
	}
	if repoMatches("https://github.com/acme/other", "acme/core") {
		t.Fatal("different repo must not match")
	}
}
