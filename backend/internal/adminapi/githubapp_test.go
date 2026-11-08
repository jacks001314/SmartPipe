package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExchangeGitHubAppManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/code-1/conversions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12345,"slug":"smartpipe-dev","pem":"-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----","client_id":"Iv1.test","owner":{"login":"jacks001314"}}`))
	}))
	defer srv.Close()

	oldAPI := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = oldAPI }()

	got, err := exchangeGitHubAppManifest(context.Background(), "code-1")
	if err != nil {
		t.Fatalf("exchange manifest: %v", err)
	}
	if got.ID != 12345 || got.Slug != "smartpipe-dev" || got.Owner.Login != "jacks001314" || got.PEM == "" {
		t.Fatalf("unexpected conversion: %+v", got)
	}
}
