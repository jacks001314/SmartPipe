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

func TestLanxinTokenUsesGetWithQuery(t *testing.T) {
	var gotMethod, gotPath string
	var gotQuery = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		for _, k := range []string{"grant_type", "appid", "secret"} {
			gotQuery[k] = r.URL.Query().Get(k)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errCode":0,"errMsg":"","data":{"appToken":"x","expiresIn":7200}}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "lanxin", "s3cret", map[string]any{
		"app_id":   "org-app",
		"api_base": srv.URL + "/",
	})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/apptoken/create" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotQuery["grant_type"] != "client_credential" || gotQuery["appid"] != "org-app" || gotQuery["secret"] != "s3cret" {
		t.Fatalf("unexpected query %+v", gotQuery)
	}
}

// 用户误把 /v1 填进 API Base 时，不应拼成 /v1/v1。
func TestLanxinBaseStripsVersionSuffix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"errCode":0,"data":{"appToken":"x"}}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "lanxin", "s3cret", map[string]any{
		"app_id":   "org-app",
		"api_base": srv.URL + "/v1/",
	})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
	if gotPath != "/v1/apptoken/create" {
		t.Fatalf("unexpected path %q", gotPath)
	}
}

// errCode 非 0 时必须带回上游 errMsg 与可操作建议，并且不泄露 secret。
func TestLanxinReportsUpstreamErrMsg(t *testing.T) {
	secret := "super-secret-app-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errCode":-5,"errMsg":"appid or secret invalid","data":null}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "lanxin", secret, map[string]any{
		"app_id":   "org-app",
		"api_base": srv.URL,
	})
	if res.OK {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"errCode=-5", "appid or secret invalid", "API Base"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message missing %q: %q", want, res.Message)
		}
	}
	if strings.Contains(res.Message, secret) {
		t.Fatal("message leaked the app secret")
	}
}

// 网关返回非 JSON（例如 HTML 错误页）时要说明原因，而不是只报状态码。
func TestLanxinNonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html>404 Not Found</html>"))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "lanxin", "s3cret", map[string]any{
		"app_id":   "org-app",
		"api_base": srv.URL,
	})
	if res.OK {
		t.Fatal("expected failure")
	}
	if !strings.Contains(res.Message, "404") || !strings.Contains(res.Message, "不是 JSON") {
		t.Fatalf("unexpected message %q", res.Message)
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

func TestModelProviderListsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "model_provider", "sk-good", map[string]any{"base_url": srv.URL})
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
	if !strings.Contains(res.Message, "1 个模型") {
		t.Fatalf("unexpected message %q", res.Message)
	}
}

func TestModelProviderDoesNotLeakKey(t *testing.T) {
	secret := "sk-super-secret-model-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Model is blocked","key":"` + secret + `"}`))
	}))
	defer srv.Close()

	res := newChecker().Dispatch(context.Background(), "model_provider", secret, map[string]any{"base_url": srv.URL})
	if res.OK {
		t.Fatal("expected failure")
	}
	if strings.Contains(res.Message, secret) {
		t.Fatal("message leaked the api key")
	}
	if !strings.Contains(res.Message, "Model is blocked") {
		t.Fatalf("upstream error was swallowed: %q", res.Message)
	}
}
