package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeExec struct {
	lastTool string
	lastArgs map[string]any
	err      error
}

func (f *fakeExec) Call(ctx context.Context, p *Principal, tool string, args map[string]any) (any, error) {
	f.lastTool, f.lastArgs = tool, args
	if f.err != nil {
		return nil, f.err
	}
	return map[string]any{"ok": true, "tool": tool, "role": p.Role}, nil
}

func newServer(role string, exec *fakeExec) *Server {
	return &Server{
		Auth: func(ctx context.Context, token string) (*Principal, error) {
			if token != "good" {
				return nil, ErrUnauthorized
			}
			pid := "prj_1"
			return &Principal{TokenID: "tok_1", Name: "lead-a", Role: role, ProjectID: &pid}, nil
		},
		Exec:      exec,
		RateLimit: 100,
	}
}

func call(t *testing.T, s *Server, body string) map[string]any {
	t.Helper()
	p, err := s.Auth(context.Background(), "good")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	out, ok, _ := s.Handle(context.Background(), p, []byte(body))
	if !ok {
		t.Fatalf("expected response for %s", body)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	return resp
}

func strPtr(s string) *string { return &s }

func TestInitializeAndToolsList(t *testing.T) {
	s := newServer("dev", &fakeExec{})
	init := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	res := init["result"].(map[string]any)
	if res["protocolVersion"] == "" || res["serverInfo"] == nil {
		t.Fatalf("initialize = %+v", res)
	}
	list := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("dev should only see project.info, got %d tools", len(tools))
	}
	if name := tools[0].(map[string]any)["name"]; name != "project.info" {
		t.Fatalf("dev tool = %v", name)
	}
	lead := newServer("lead", &fakeExec{})
	list = call(t, lead, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if got := len(list["result"].(map[string]any)["tools"].([]any)); got != len(Tools) {
		t.Fatalf("lead should see %d tools, got %d", len(Tools), got)
	}
}

func TestToolCallPermissionAndResult(t *testing.T) {
	exec := &fakeExec{}
	dev := newServer("dev", exec)
	resp := call(t, dev, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"derivation.create","arguments":{}}}`)
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("dev must be denied: %+v", result)
	}
	if exec.lastTool != "" {
		t.Fatal("denied tool must not reach executor")
	}

	lead := newServer("lead", exec)
	resp = call(t, lead, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"project.info","arguments":{"project_id":"prj_1"}}}`)
	result = resp["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("lead call failed: %+v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, `"ok": true`) || exec.lastTool != "project.info" {
		t.Fatalf("content = %s, tool = %s", content, exec.lastTool)
	}

	exec.err = errors.New("boom: approval_required")
	resp = call(t, lead, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"verify.patch","arguments":{}}}`)
	if resp["result"].(map[string]any)["isError"] != true {
		t.Fatalf("executor error should surface as isError")
	}
}

func TestRateLimit(t *testing.T) {
	exec := &fakeExec{}
	s := newServer("lead", exec)
	s.RateLimit = 1
	call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project.info"}}`)
	resp := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"project.info"}}`)
	if resp["error"] == nil {
		t.Fatalf("expected rate limit error, got %+v", resp)
	}
}

func TestHTTPHandler(t *testing.T) {
	s := newServer("lead", &fakeExec{})
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer good")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "project.info") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set("Authorization", "Bearer good")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d", w.Code)
	}
}
