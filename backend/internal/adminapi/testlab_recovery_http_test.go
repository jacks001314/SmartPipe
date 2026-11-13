package adminapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopRecoveryHTTPRequiresConfirmationAndCurrentGeneration(t *testing.T) {
	const token = "synthetic-admin-test-token"
	app, st := newTestAPI(t, token)
	const machine = "recovery-http-machine"
	if err := st.RegisterDesktopMachine(t.Context(), machine, "windows-amd64", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.DB().Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine) })
	machines, err := st.ListDesktopMachines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	for _, m := range machines {
		if m.ID == machine {
			generation = m.Generation
		}
	}
	call := func(body any, auth string) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/admin/test-machines/"+machine+"/recover", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		return rr.Code
	}
	body := map[string]any{"generation": generation, "cleanup_confirmed": true, "reason": "local processes stopped and resources removed"}
	if code := call(body, ""); code != 401 {
		t.Fatalf("anonymous=%d", code)
	}
	if code := call(body, token); code != 409 {
		t.Fatalf("enabled machine=%d", code)
	}
	if err = st.SetDesktopMachineEnabled(t.Context(), machine, false); err != nil {
		t.Fatal(err)
	}
	body["cleanup_confirmed"] = false
	if code := call(body, token); code != 400 {
		t.Fatalf("unconfirmed=%d", code)
	}
	body["cleanup_confirmed"] = true
	body["generation"] = generation + 1
	if code := call(body, token); code != 409 {
		t.Fatalf("wrong generation=%d", code)
	}
	body["generation"] = generation
	if code := call(body, token); code != 204 {
		t.Fatalf("recovery=%d", code)
	}
	if code := call(body, token); code != 409 {
		t.Fatalf("replayed recovery=%d", code)
	}
	machines, err = st.ListDesktopMachines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range machines {
		if m.ID == machine {
			if m.Enabled || m.RunID != "" || m.Generation != generation+1 {
				t.Fatalf("unexpected recovered machine: %+v", m)
			}
			return
		}
	}
	t.Fatal(testlab.ErrState)
}
