package releasesvc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func completeRequest(t *testing.T, h *HTTPHandler, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/release-executors/linux-01/complete", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer svc")
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// 对象存储暂时不可用时执行器报 retryable：目标退回队列，不把运行判死。
func TestCompleteRequeuesRetryableFailure(t *testing.T) {
	fake := &fakeControlStore{requeueOK: true}
	h := &HTTPHandler{Store: fake, Token: "svc", MaxTargetAttempts: 3}
	code := completeRequest(t, h, `{"run_id":"rrun_1","target_id":"linux-amd64","ok":false,"retryable":true,"error_code":"artifact_upload_failed","error_message":"put: connection refused"}`)
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	if len(fake.requeueCalls) != 1 {
		t.Fatalf("requeue calls = %v, want 1", fake.requeueCalls)
	}
	if len(fake.finishCalls) != 0 {
		t.Fatalf("finish should not be called on retryable failure, got %v", fake.finishCalls)
	}
	var sawRetry bool
	for _, e := range fake.events {
		if e.Kind == "target.retry" {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Fatalf("missing target.retry event: %+v", fake.events)
	}
}

// 重试次数用尽后再失败：按真实失败收口。
func TestCompleteFailsWhenAttemptsExhausted(t *testing.T) {
	fake := &fakeControlStore{requeueOK: false}
	h := &HTTPHandler{Store: fake, Token: "svc", MaxTargetAttempts: 3}
	code := completeRequest(t, h, `{"run_id":"rrun_1","target_id":"linux-amd64","ok":false,"retryable":true,"error_code":"artifact_upload_failed","error_message":"put: connection refused"}`)
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	if len(fake.requeueCalls) != 1 {
		t.Fatalf("requeue calls = %v, want 1 attempt", fake.requeueCalls)
	}
	if len(fake.finishCalls) != 1 {
		t.Fatalf("finish calls = %v, want 1", fake.finishCalls)
	}
	if !strings.Contains(fake.finishCalls[0], "ok=false") {
		t.Fatalf("finish call should record failure: %v", fake.finishCalls)
	}
}

// 构建失败不可重试，直接收口。
func TestCompleteSkipsRequeueForNonRetryable(t *testing.T) {
	fake := &fakeControlStore{requeueOK: true}
	h := &HTTPHandler{Store: fake, Token: "svc"}
	code := completeRequest(t, h, `{"run_id":"rrun_1","target_id":"linux-amd64","ok":false,"error_code":"target_failed","error_message":"build: exit status 7"}`)
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	if len(fake.requeueCalls) != 0 {
		t.Fatalf("non-retryable failure should not requeue: %v", fake.requeueCalls)
	}
	if len(fake.finishCalls) != 1 {
		t.Fatalf("finish calls = %v, want 1", fake.finishCalls)
	}
}
