package releasesvc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestExecutorCommandNextAndResult(t *testing.T) {
	fake := &fakeControlStore{commands: []*releasev2.ExecutorCommand{{
		ID: 7, ExecutorID: "mac-01", RunID: "run_1", Kind: releasev2.ExecutorCommandProbe,
		Status: releasev2.ExecutorCommandRunning,
	}}}
	handler := &HTTPHandler{Store: fake, Token: "svc-token"}
	server := httptest.NewServer(handler.Handler())
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/internal/release-executors/mac-01/commands/next?wait_seconds=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer svc-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("next status = %d", resp.StatusCode)
	}
	var cmd releasev2.ExecutorCommand
	if err := json.NewDecoder(resp.Body).Decode(&cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ID != 7 || cmd.Kind != releasev2.ExecutorCommandProbe {
		t.Fatalf("command = %+v", cmd)
	}

	body, _ := json.Marshal(map[string]any{"exit_code": 0, "output": "ok"})
	resultReq, err := http.NewRequest(http.MethodPost, server.URL+"/internal/release-executors/mac-01/commands/7/result", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resultReq.Header.Set("Authorization", "Bearer svc-token")
	resultResp, err := http.DefaultClient.Do(resultReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resultResp.Body.Close()
	if resultResp.StatusCode != http.StatusNoContent {
		t.Fatalf("result status = %d", resultResp.StatusCode)
	}
	if len(fake.completed) != 1 || fake.completed[0] != "7/mac-01/0/ok" {
		t.Fatalf("completed = %v", fake.completed)
	}
}

func TestExecutorCommandNextTimesOut(t *testing.T) {
	handler := &HTTPHandler{Store: &fakeControlStore{}, Token: "svc-token"}
	server := httptest.NewServer(handler.Handler())
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/internal/release-executors/mac-01/commands/next?wait_seconds=0", nil)
	req.Header.Set("Authorization", "Bearer svc-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
