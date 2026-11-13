package adminapi

import (
	"github.com/kataras/iris/v12"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTestLabRequiresAdmin(t *testing.T) {
	app := iris.New()
	a := New(Config{AdminToken: "test-only"})
	p := app.Party("/admin")
	p.Use(a.auth)
	a.registerTestLab(p)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/test-runs", "/admin/test-runs/x/start", "/admin/test-runs/x/report", "/admin/test-runs/x/cancel"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, rr.Code)
		}
	}
}
func TestTestLabRejectsMalformedReport(t *testing.T) {
	app := iris.New()
	a := New(Config{AdminToken: "test-only"})
	p := app.Party("/admin")
	p.Use(a.auth)
	a.registerTestLab(p)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/test-runs/x/report", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer test-only")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatal(rr.Code)
	}
}
