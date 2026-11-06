package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeModelSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer srv.Close()

	ok, message, _ := probeModel(context.Background(), srv.URL+"/v1", "sk-test", "deepseek-v4-flash")
	if !ok {
		t.Fatalf("expected ok, got %q", message)
	}
	if !strings.Contains(message, "OK") {
		t.Fatalf("unexpected message %q", message)
	}
}

func TestProbeModelSurfacesUpstreamError(t *testing.T) {
	secret := "sk-abc-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Model is blocked","key":"` + secret + `"}`))
	}))
	defer srv.Close()

	ok, message, _ := probeModel(context.Background(), srv.URL, secret, "bad-model")
	if ok {
		t.Fatal("expected failure")
	}
	if strings.Contains(message, secret) {
		t.Fatal("message leaked the key")
	}
	if !strings.Contains(message, "Model is blocked") {
		t.Fatalf("upstream error missing: %q", message)
	}
}
