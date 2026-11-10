package bridge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"smartpipe/backend/internal/store"
)

// appServer 模拟一个只装了部分账号的 GitHub App：安装令牌能换到，
// 但安装范围之外的仓库一律 404。
func appServer(t *testing.T, repoVisible bool) (*httptest.Server, *int32) {
	t.Helper()
	var tokens int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && len(r.URL.Path) > 6 && r.URL.Path[len(r.URL.Path)-6:] == "tokens":
			atomic.AddInt32(&tokens, 1)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      "ghs_app_token",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case r.Method == http.MethodGet && repoVisible:
			_, _ = w.Write([]byte(`{"full_name":"example/core","private":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &tokens
}

// patServer 模拟用户 PAT 能看到仓库。
func patServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`{"full_name":"example/core","private":true}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/example/core/issues/7"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func appKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestClientForRepoFallsBackToPAT(t *testing.T) {
	env := newEnv(t)
	ResetCredentialCache()
	t.Cleanup(ResetCredentialCache)
	ctx := context.Background()
	project := env.project(t, true)

	app, _ := appServer(t, false)
	pat, patHits := patServer(t)

	if _, err := env.store.CreateCredential(ctx, &project.ID, store.CredentialGitHubApp, "app", appKeyPEM(t), map[string]any{
		"api_base":        app.URL,
		"app_id":          "1",
		"installation_id": "2",
	}); err != nil {
		t.Fatalf("app credential: %v", err)
	}
	if _, err := env.store.CreateCredential(ctx, &project.ID, store.CredentialGitHubPAT, "pat", "ghp_user_token", map[string]any{
		"api_base": pat.URL,
	}); err != nil {
		t.Fatalf("pat credential: %v", err)
	}

	client, err := ClientForRepo(ctx, env.store, &project.ID, "example/core")
	if err != nil {
		t.Fatalf("ClientForRepo: %v", err)
	}
	if client.Token != "ghp_user_token" {
		t.Fatalf("expected fallback to PAT, got token %q", client.Token)
	}
	if _, err := client.CreateIssue(ctx, "example/core", "标题", "正文", nil); err != nil {
		t.Fatalf("create issue via fallback: %v", err)
	}
	if atomic.LoadInt32(patHits) == 0 {
		t.Fatal("expected PAT endpoint to be used")
	}
	if _, err := ClientForRepo(ctx, env.store, &project.ID, "example/core"); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
}

func TestClientForRepoPrefersAppWhenRepoVisible(t *testing.T) {
	env := newEnv(t)
	ResetCredentialCache()
	t.Cleanup(ResetCredentialCache)
	ctx := context.Background()
	project := env.project(t, true)

	app, appTokens := appServer(t, true)
	if _, err := env.store.CreateCredential(ctx, &project.ID, store.CredentialGitHubApp, "app", appKeyPEM(t), map[string]any{
		"api_base":        app.URL,
		"app_id":          "1",
		"installation_id": "2",
	}); err != nil {
		t.Fatalf("app credential: %v", err)
	}

	client, err := ClientForRepo(ctx, env.store, &project.ID, "example/core")
	if err != nil {
		t.Fatalf("ClientForRepo: %v", err)
	}
	if client.Token != "ghs_app_token" {
		t.Fatalf("expected app installation token, got %q", client.Token)
	}
	if atomic.LoadInt32(appTokens) != 1 {
		t.Fatalf("installation token issued %d times, want cached single call", *appTokens)
	}
}
