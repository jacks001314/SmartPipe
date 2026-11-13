package adminapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopMachineHTTPProtocol(t *testing.T) {
	app, st := newTestAPI(t, "")
	ctx := t.Context()
	p, err := st.CreateProject(ctx, &store.Project{Name: "machine-http"})
	if err != nil {
		t.Fatal(err)
	}
	const machine = "protocol-machine"
	token := strings.Repeat("x", 64) // Synthetic test token, never a deployment credential.
	if err = st.RegisterDesktopMachine(ctx, machine, "windows-amd64", testlab.TokenDigest(token)); err != nil {
		t.Fatal(err)
	}
	const run = "protocol-run"
	t.Cleanup(func() {
		_, _ = st.DB().Exec("DELETE FROM desktop_test_runs WHERE id=$1", run)
		_, _ = st.DB().Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
	})
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64),
		Scenario: "desktop-observe", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"desktop_observed"}}
	if _, err = st.CreateDesktopTestRun(ctx, run, spec); err != nil {
		t.Fatal(err)
	}
	call := func(path string, body any, auth string) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest(http.MethodPost, "/test-runner/"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Machine-ID", machine)
		req.Header.Set("Authorization", "Bearer "+auth)
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		return rr
	}
	expect := func(rr *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rr.Code != status {
			t.Fatalf("HTTP status %d, want %d", rr.Code, status)
		}
	}
	expect(call("claim", map[string]any{"scenarios": []string{"desktop-observe@1"}}, strings.Repeat("y", 64)), 401)
	claim := call("claim", map[string]any{"scenarios": []string{"desktop-observe@1"}}, token)
	expect(claim, 200)
	var envelope struct{ Data testlab.Lease }
	if err = json.Unmarshal(claim.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	lease := envelope.Data
	if lease.Run == nil || lease.Run.ID != run || lease.Generation < 1 {
		t.Fatal("invalid assignment")
	}
	assignment := map[string]any{"run_id": run, "generation": lease.Generation}
	expect(call("heartbeat", assignment, token), 200)
	expect(call("release", assignment, token), 409)
	payload := []byte("protocol evidence")
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	report := testlab.Report{SourceSHA: spec.SourceSHA, ArtifactSHA256: spec.ArtifactSHA256, ScenarioVersion: "1", Platform: spec.Platform,
		Checks: []testlab.Check{{ID: "desktop_observed", Status: "PASS", Evidence: "sha256:" + digest}}}
	assignment["report"] = report
	if rr := call("report", assignment, token); rr.Code == 200 {
		t.Fatal("accepted report before evidence")
	}
	expect(call("evidence", map[string]any{"run_id": run, "generation": lease.Generation,
		"evidence": store.DesktopEvidence{SHA256: digest, MIMEType: "text/plain", Payload: payload}}, token), 200)
	expect(call("report", assignment, token), 200)
	expect(call("report", assignment, token), 409)
	expect(call("release", assignment, token), 204)
	persisted, err := st.GetDesktopTestRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "PASS" {
		t.Fatal("result not persisted")
	}
	machines, err := st.ListDesktopMachines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range machines {
		if m.ID == machine && m.RunID != "" {
			t.Fatal("machine not released")
		}
	}
}
