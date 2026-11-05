package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"smartpipe/sdks/lanxin"
)

type fakeMedia struct {
	data  []byte
	err   error
	calls int32
}

func (f *fakeMedia) DownloadMedia(ctx context.Context, mediaID string) ([]byte, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

func validBundle() []byte {
	return []byte(`{"app":"desktop-client","version":"4.2.1","os":"Windows 11","arch":"amd64",
"exception":{"type":"NullPointer","message":"点击登录崩溃","stack":"at Login.Show()\nat Main()"}}`)
}

func TestCrashIntakeRejectsOversizedAttachment(t *testing.T) {
	var posted int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posted, 1)
		_, _ = w.Write([]byte(`{"data":{"status":"created","issue_number":1}}`))
	}))
	defer srv.Close()

	media := &fakeMedia{data: []byte(strings.Repeat("x", 2048))}
	ci := &CrashIntake{IngestURL: srv.URL, MaxBytes: 1024, Media: media}
	res, msg, err := ci.HandleAttachment(context.Background(), lanxin.MediaFile, "m-1", "grp-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil {
		t.Fatal("oversized upload must not return a result")
	}
	if !strings.Contains(msg, "超过上限") || !strings.Contains(msg, "1024") {
		t.Fatalf("message = %s", msg)
	}
	if atomic.LoadInt32(&posted) != 0 {
		t.Fatal("oversized upload must not call the ingest endpoint")
	}
}

func TestCrashIntakeRejectsInvalidJSONAndMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach ingest for invalid bundles")
	}))
	defer srv.Close()
	ci := &CrashIntake{IngestURL: srv.URL, Media: &fakeMedia{data: []byte("not-json")}}
	if _, msg, _ := ci.HandleAttachment(context.Background(), lanxin.MediaFile, "m-1", "grp-test"); !strings.Contains(msg, "不是合法的 crash_bundle") {
		t.Fatalf("message = %s", msg)
	}
	ci.Media = &fakeMedia{data: []byte(`{"app":"x"}`)}
	if _, msg, _ := ci.HandleAttachment(context.Background(), lanxin.MediaFile, "m-1", "grp-test"); !strings.Contains(msg, "exception.type") {
		t.Fatalf("message = %s", msg)
	}
}

func TestCrashIntakePostsBundleAndSummarises(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Ingest-Token") != "tok-ingest" {
			t.Errorf("missing ingest token: %v", r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"status":"created","issue_number":107,"issue_url":"https://github.com/example/core/issues/107","occurrences":1}}`))
	}))
	defer srv.Close()

	media := &fakeMedia{data: validBundle()}
	ci := &CrashIntake{IngestURL: srv.URL, Token: "tok-ingest", ProjectID: "prj_1", Media: media}
	res, msg, err := ci.HandleAttachment(context.Background(), lanxin.MediaFile, "m-9", "grp-test")
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if res == nil || res.Status != "created" || res.IssueNumber != 107 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(msg, "#107") || !strings.Contains(msg, "issues/107") {
		t.Fatalf("summary = %s", msg)
	}
	if body["project_id"] != "prj_1" || body["source"] != "lanxin" {
		t.Fatalf("payload = %+v", body)
	}
	bundle, _ := body["bundle"].(map[string]any)
	if bundle["app"] != "desktop-client" {
		t.Fatalf("bundle not forwarded: %+v", bundle)
	}
}

func TestHandlerRoutesAttachmentToCrashIntake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"status":"deduped","issue_number":107,"occurrences":3,"issue_url":"u"}}`))
	}))
	defer srv.Close()

	snd := &fakeSender{}
	h := NewHandler(Config{RequireAtMe: true}, snd, AckDispatcher{}).
		WithCrash(&CrashIntake{IngestURL: srv.URL, Media: &fakeMedia{data: validBundle()}})

	ev := lanxin.Event{Type: lanxin.EventGroupMessage, Data: json.RawMessage(`{
		"msgType":"text","chatType":"group","from":"u-1","msgId":"msg-crash-1","groupId":"grp-1",
		"msgData":{"text":{"content":"","mediaType":3,"mediaIds":["media-1"]}},
		"reminder":{"isAtMe":true}}`)}
	if err := h.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	if len(snd.groups) != 1 {
		t.Fatalf("expected one group reply, got %d", len(snd.groups))
	}
	text := fmt.Sprint(snd.groups[0].MsgData)
	if !strings.Contains(text, "第 3 次出现") {
		t.Fatalf("reply = %s", text)
	}
}

func TestHandlerIgnoresAttachmentWithoutAtMe(t *testing.T) {
	snd := &fakeSender{}
	media := &fakeMedia{data: validBundle()}
	h := NewHandler(Config{RequireAtMe: true}, snd, AckDispatcher{}).
		WithCrash(&CrashIntake{IngestURL: "http://127.0.0.1:1", Media: media})
	ev := lanxin.Event{Type: lanxin.EventGroupMessage, Data: json.RawMessage(`{
		"msgType":"text","chatType":"group","from":"u-1","msgId":"msg-crash-2","groupId":"grp-1",
		"msgData":{"text":{"content":"","mediaType":3,"mediaIds":["media-1"]}}}`)}
	if err := h.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if atomic.LoadInt32(&media.calls) != 0 || len(snd.groups) != 0 {
		t.Fatal("attachment without @me must be ignored")
	}
}

func TestCrashHelpCommand(t *testing.T) {
	snd := &fakeSender{}
	h := NewHandler(Config{}, snd, AckDispatcher{}).WithCrash(&CrashIntake{Media: &fakeMedia{}})
	ev := lanxin.Event{Type: lanxin.EventP2PMessage, Data: json.RawMessage(`{
		"msgType":"text","chatType":"p2p","from":"u-1","msgId":"msg-help","msgData":{"text":{"content":"/crash"}}}`)}
	if err := h.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(snd.p2ps) != 1 || !strings.Contains(fmt.Sprint(snd.p2ps[0].MsgData), "crash_bundle") {
		t.Fatalf("help reply missing: %+v", snd.p2ps)
	}
}
