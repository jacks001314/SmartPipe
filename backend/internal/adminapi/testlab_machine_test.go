package adminapi

import (
	"github.com/kataras/iris/v12"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMachineEndpointsNeverUseAdminDevOpen(t *testing.T) {
	app := iris.New()
	a := New(Config{DevOpen: true})
	p := app.Party("/admin")
	p.Use(a.auth)
	a.registerTestMachines(p, app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"claim", "heartbeat", "report", "release"} {
		req := httptest.NewRequest(http.MethodPost, "/test-runner/"+path, nil)
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, rr.Code)
		}
	}
}
