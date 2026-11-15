package adminapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kataras/iris/v12"
)

func probeExecutorServiceURL(t *testing.T, api *API, mutate func(*http.Request)) string {
	t.Helper()
	app := iris.New()
	app.Get("/probe", func(c iris.Context) {
		_, _ = c.WriteString(api.executorServiceURL(c))
	})
	if err := app.Build(); err != nil {
		t.Fatalf("build app: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestExecutorServiceURLUsesForwardedHostAndScheme(t *testing.T) {
	t.Setenv("SMARTPIPE_RELEASE_SERVICE_PUBLIC_URL", "")
	t.Setenv("SMARTPIPE_RELEASE_SERVICE_PORT", "")
	api := &API{releaseURL: "http://worker:8095"}

	got := probeExecutorServiceURL(t, api, func(req *http.Request) {
		req.Header.Set("X-Forwarded-Host", "release.example.com")
		req.Header.Set("X-Forwarded-Proto", "https")
	})
	if got != "https://release.example.com:8099" {
		t.Fatalf("forwarded url = %q", got)
	}

	got = probeExecutorServiceURL(t, api, func(req *http.Request) {
		req.Host = "build-console.example.com:8092"
	})
	if got != "http://build-console.example.com:8099" {
		t.Fatalf("direct url = %q", got)
	}
}

func TestExecutorServiceURLPrefersPublicURL(t *testing.T) {
	t.Setenv("SMARTPIPE_RELEASE_SERVICE_PUBLIC_URL", "https://releases.example.com/")
	api := &API{releaseURL: "http://worker:8095"}

	got := probeExecutorServiceURL(t, api, func(req *http.Request) {
		req.Header.Set("X-Forwarded-Host", "ignored.example.com")
	})
	if got != "https://releases.example.com" {
		t.Fatalf("public url = %q", got)
	}
}
