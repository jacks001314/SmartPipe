package conncheck

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newChecker() *Checker { return New() }

func TestGitHubPATSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "github_pat", "good-token", map[string]any{"api_base": srv.URL})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
}

func TestGitHubPATReportsStatusWithoutLeakingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	secret := "ghp_supersecrettokenvalue"
	res := newChecker().Dispatch(context.Background(), "github_pat", secret, map[string]any{"api_base": srv.URL})
	if res.OK {
		t.Fatal("expected failure")
	}
	if strings.Contains(res.Message, secret) {
		t.Fatal("message leaked the token")
	}
}

func TestSyntropyHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "syntropy", "", map[string]any{"base_url": srv.URL})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}

	down := newChecker().Dispatch(context.Background(), "syntropy", "", map[string]any{"base_url": "http://127.0.0.1:1"})
	if down.OK {
		t.Fatal("expected failure for closed port")
	}
}

func TestLanxinToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errCode":0,"data":{"appToken":"x"}}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "lanxin", "s3cret", map[string]any{
		"app_id":   "org-app",
		"api_base": srv.URL,
	})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
}

func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubAppExchangesInstallationToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_x"}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "github_app", testPrivateKeyPEM(t), map[string]any{
		"app_id":          "12345",
		"installation_id": "6789",
		"api_base":        srv.URL,
	})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
	if !strings.HasPrefix(gotAuth, "Bearer eyJ") {
		t.Fatalf("expected JWT bearer, got %q", gotAuth)
	}
}

func TestGitHubAppMissingMetadata(t *testing.T) {
	res := newChecker().Dispatch(context.Background(), "github_app", testPrivateKeyPEM(t), map[string]any{})
	if res.OK {
		t.Fatal("expected failure without app_id/installation_id")
	}
}

func TestUnknownKind(t *testing.T) {
	res := newChecker().Dispatch(context.Background(), "nope", "x", nil)
	if res.OK {
		t.Fatal("expected failure")
	}
}

func TestDispatchRecordsLatency(t *testing.T) {
	c := New()
	c.Now = func() time.Time { return time.Unix(1000, 0) }
	res := c.Dispatch(context.Background(), "github_webhook_secret", "0123456789abcdef", nil)
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
	if res.Latency < 0 {
		t.Fatalf("negative latency: %d", res.Latency)
	}
}
