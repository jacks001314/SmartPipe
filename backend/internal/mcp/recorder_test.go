package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakeRecorder struct {
	mu   sync.Mutex
	recs []CallRecord
}

func (f *fakeRecorder) Record(ctx context.Context, rec CallRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, rec)
}

func (f *fakeRecorder) all() []CallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CallRecord, len(f.recs))
	copy(out, f.recs)
	return out
}

func TestRecorderCapturesDeniedAndOK(t *testing.T) {
	rec := &fakeRecorder{}
	exec := &fakeExec{}
	dev := newServer("dev", exec)
	dev.Recorder = rec
	call(t, dev, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"derivation.create","arguments":{"issue_ref":"acme/app#7"}}}`)
	recs := rec.all()
	if len(recs) != 1 {
		t.Fatalf("denied call should be recorded, got %d", len(recs))
	}
	if recs[0].Status != "denied" || recs[0].Tool != "derivation.create" || recs[0].Role != "dev" {
		t.Fatalf("record = %+v", recs[0])
	}
	if recs[0].TokenID != "tok_1" || recs[0].ProjectID == nil || *recs[0].ProjectID != "prj_1" {
		t.Fatalf("principal not carried: %+v", recs[0])
	}

	lead := newServer("lead", exec)
	lead.Recorder = rec
	call(t, lead, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"project.info","arguments":{}}}`)
	recs = rec.all()
	if len(recs) != 2 || recs[1].Status != "ok" {
		t.Fatalf("records = %+v", recs)
	}

	exec.err = context.DeadlineExceeded
	call(t, lead, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"verify.patch","arguments":{}}}`)
	recs = rec.all()
	if len(recs) != 3 || recs[2].Status != "error" || recs[2].Error == "" {
		t.Fatalf("error record = %+v", recs)
	}
}

func TestSummarizeArgsDropsPatchAndBody(t *testing.T) {
	// 补丁原文与评论正文都不应进入审计摘要
	rec := &fakeRecorder{}
	exec := &fakeExec{}
	s := newServer("lead", exec)
	s.Recorder = rec
	call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"verify.patch","arguments":{"issue_ref":"acme/app#7","version":2,"patch":"diff --git a/secret.txt b/secret.txt\n+TOKEN=leak-me\n","body":"这是一大段评论正文，包含内部细节","labels":["a","b"]}}}`)
	recs := rec.all()
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	summary := recs[0].ArgsSummary
	if summary["issue_ref"] != "acme/app#7" {
		t.Fatalf("issue_ref missing: %+v", summary)
	}
	for k, v := range summary {
		if k == "patch" || k == "body" {
			t.Fatalf("summary must not contain %s: %+v", k, v)
		}
		if s, ok := v.(string); ok && (strings.Contains(s, "TOKEN=") || strings.Contains(s, "评论正文")) {
			t.Fatalf("summary leaked payload for %s", k)
		}
	}
	if summary["labels_count"] != 2 {
		t.Fatalf("labels_count = %v", summary["labels_count"])
	}
	if summary["patch_bytes"] == nil || summary["body_bytes"] == nil {
		t.Fatalf("expected byte counts: %+v", summary)
	}
}
