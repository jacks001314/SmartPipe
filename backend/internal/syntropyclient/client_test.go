package syntropyclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "session-token")
}

func TestHealthDegradedIsNotAnError(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "syntropy_desktop_session=session-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","version":"0.9.1","dependencies":{"master":{"status":"unconfigured","configured":false}}}`))
	})

	body, status, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", status)
	}
	if body["status"] != "degraded" {
		t.Fatalf("unexpected body: %v", body)
	}
	if body["version"] != "0.9.1" {
		t.Fatalf("version not parsed: %v", body)
	}
}

func TestUnauthorized(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, _, err := c.Health(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAgentListParsesItems(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"agents":[{"id":"a1","name":"lead-core","phase":"AGENT_PHASE_RUNNING"}],"total":1}`))
	})

	agents, err := c.AgentList(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(agents) != 1 || agents[0]["id"] != "a1" {
		t.Fatalf("unexpected agents: %+v", agents)
	}
	if agents[0]["phase"] != "AGENT_PHASE_RUNNING" {
		t.Fatalf("phase mismatch: %v", agents[0]["phase"])
	}
}

func TestDeployAgentSendsSpecAndReturnsOperation(t *testing.T) {
	var captured map[string]any
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agents" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"id":"op-1","agentId":"a-9","state":"OPERATION_STATE_PENDING"}`))
	})

	out, err := c.DeployAgent(context.Background(), AgentSpec{
		ProjectID:    "prj_1",
		Name:         "lead-core-01",
		TemplateName: "pipeline-lead-v3",
		TargetNodeID: "node-1",
	}, "req-1")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if out["id"] != "op-1" {
		t.Fatalf("unexpected operation: %v", out)
	}
	spec, _ := captured["spec"].(map[string]any)
	if spec["project_id"] != "prj_1" || spec["template_name"] != "pipeline-lead-v3" {
		t.Fatalf("spec not sent correctly: %v", captured)
	}
	if spec["runtime"] != "AGENT_RUNTIME_TYPE_CODEX" {
		t.Fatalf("runtime default missing: %v", spec["runtime"])
	}
	if captured["requestId"] != "req-1" {
		t.Fatalf("requestId missing: %v", captured["requestId"])
	}
}

func TestNodeListPathAndStatusError(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/node/list/2/10" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("bad path " + r.URL.Path))
			return
		}
		if r.URL.Query().Get("masterId") != "m1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"nodes":[{"id":"n1","name":"worker-dev","status":"online"}]}`))
	})

	out, err := c.NodeList(context.Background(), "m1", 2, 10)
	if err != nil {
		t.Fatalf("node list: %v", err)
	}
	nodes, _ := out["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("unexpected nodes: %v", out)
	}

	_, err = c.NodeList(context.Background(), "", 2, 10)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Fatalf("expected StatusError 400, got %v", err)
	}
}

func TestStatusErrorTruncatesBody(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	})
	_, err := c.MasterStatus(context.Background())
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected StatusError, got %v", err)
	}
	if len(se.Error()) > 400 {
		t.Fatalf("error message not truncated: %d chars", len(se.Error()))
	}
}
