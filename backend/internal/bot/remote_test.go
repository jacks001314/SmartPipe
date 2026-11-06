package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchRemoteConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "bot-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"app_id":"app-1","app_secret":"sec-1","api_base":"https://lanxin.example","require_at_me":false}}`))
	}))
	defer srv.Close()

	rc, err := FetchRemoteConfig(context.Background(), srv.URL, "bot-token")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if rc == nil || rc.AppID != "app-1" || rc.AppSecret != "sec-1" || rc.RequireAtMe {
		t.Fatalf("unexpected config: %+v", rc)
	}
	cfg := Config{AppID: "old", AppSecret: "old-secret", APIBase: "old", RequireAtMe: true}
	merged := cfg.WithRemote(rc)
	if merged.AppID != "app-1" || merged.RequireAtMe {
		t.Fatalf("unexpected merged config: %+v", merged)
	}
	if merged.SameLanxin(rc) != true {
		t.Fatal("expected same config")
	}
}

func TestFetchRemoteConfigNotConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	rc, err := FetchRemoteConfig(context.Background(), srv.URL, "token")
	if err != nil || rc != nil {
		t.Fatalf("expected nil,nil got %+v %v", rc, err)
	}
}