package bridge

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const secret = "topsecret-webhook-key"

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	good := Sign(secret, body)
	if !VerifySignature(secret, body, good) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature(secret, []byte("tampered"), good) {
		t.Fatal("tampered body accepted")
	}
	if VerifySignature("other", body, good) {
		t.Fatal("wrong secret accepted")
	}
	for _, bad := range []string{"", "sha1=abc", "sha256=zzzz", "sha256="} {
		if VerifySignature(secret, body, bad) {
			t.Fatalf("malformed signature %q accepted", bad)
		}
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	h := &WebhookHandler{Secret: secret}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader([]byte("{}")))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestWebhookAcceptsValidSignature(t *testing.T) {
	h := &WebhookHandler{Secret: secret}
	body := []byte(`{"action":"opened","repository":{"full_name":"o/r"},"issue":{"number":3}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", Sign(secret, body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "accepted") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestWebhookRejectsWrongMethod(t *testing.T) {
	h := &WebhookHandler{Secret: secret}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhooks/github", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}
