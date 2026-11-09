package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type captureTransport struct {
	path  string
	token string
	body  []byte
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.path = req.URL.Path
	c.token = req.Header.Get("X-Scheduler-Token")
	c.body, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: http.StatusAccepted,
		Status:     "202 Accepted",
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// bot → scheduler 的 HTTP 载荷必须带上附件，字节以 base64 往返不变。
func TestSchedulerDispatcherSerializesAttachments(t *testing.T) {
	rt := &captureTransport{}
	d := SchedulerDispatcher{BaseURL: "http://scheduler.local", Token: "tok", Client: &http.Client{Transport: rt}}
	payload := []byte("title = \"smartpipe\"\n")
	err := d.Dispatch(context.Background(), Inbound{
		Platform: "lanxin", PlatformMessageID: "m-1", ChatType: "group", GroupID: "g-1",
		SenderID: "u-1", Text: "文件内容是什么", BotID: "bot-1",
		Attachments: []Attachment{{Name: "config.toml", MimeType: "text/plain", Data: payload}},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if rt.path != "/internal/scheduler/messages" || rt.token != "tok" {
		t.Fatalf("path=%s token=%s", rt.path, rt.token)
	}
	var got struct {
		Attachments []struct {
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
			Data     []byte `json:"data"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(rt.body, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	att := got.Attachments[0]
	if att.Name != "config.toml" || att.MimeType != "text/plain" || string(att.Data) != string(payload) {
		t.Fatalf("attachment = %+v", att)
	}
}
