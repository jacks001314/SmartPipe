package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type adminCall struct {
	method string
	path   string
	body   map[string]any
}

func fakeAdmin(t *testing.T, calls *[]adminCall, resp string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		*calls = append(*calls, adminCall{method: r.Method, path: r.URL.RequestURI(), body: body})
		if strings.HasPrefix(r.URL.Path, "/admin/projects/missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"记录不存在"}}`))
			return
		}
		_, _ = w.Write([]byte(resp))
	}))
}

func TestAdminBackendMapsTools(t *testing.T) {
	var calls []adminCall
	srv := fakeAdmin(t, &calls, `{"data":{"ok":true}}`)
	defer srv.Close()
	backend := NewAdminBackend(srv.URL, "admin-token")
	pid := "prj_1"
	p := &Principal{TokenID: "tok", Role: "lead", ProjectID: &pid}
	ctx := context.Background()

	if _, err := backend.Call(ctx, p, "issue.comment", map[string]any{"number": float64(12), "body": "方案 v1"}); err != nil {
		t.Fatalf("comment: %v", err)
	}
	last := calls[len(calls)-1]
	if last.method != http.MethodPost || last.path != "/admin/issues/12/comments" {
		t.Fatalf("call = %+v", last)
	}
	if last.body["project_id"] != "prj_1" || last.body["body"] != "方案 v1" {
		t.Fatalf("body = %+v", last.body)
	}

	if _, err := backend.Call(ctx, p, "release.run", map[string]any{"version": "v1.2.3", "dry_run": true}); err != nil {
		t.Fatalf("release: %v", err)
	}
	last = calls[len(calls)-1]
	if last.path != "/admin/releases/runs" || last.body["dry_run"] != true || last.body["version"] != "v1.2.3" {
		t.Fatalf("release call = %+v", last)
	}

	if _, err := backend.Call(ctx, p, "issue.add_labels", map[string]any{"number": float64(7), "labels": []any{"smartpipe:ready"}}); err != nil {
		t.Fatalf("labels: %v", err)
	}
	last = calls[len(calls)-1]
	labels, _ := last.body["labels"].([]any)
	if last.path != "/admin/issues/7/labels" || len(labels) != 1 || labels[0] != "smartpipe:ready" {
		t.Fatalf("labels call = %+v", last)
	}

	if _, err := backend.Call(ctx, p, "project.info", map[string]any{"project_id": "prj_2"}); err == nil {
		t.Fatal("project scope must be enforced")
	}
	unbound := &Principal{TokenID: "tok", Role: "lead"}
	if _, err := backend.Call(ctx, unbound, "project.info", map[string]any{"project_id": "missing"}); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("error envelope not surfaced: %v", err)
	}
	if _, err := backend.Call(ctx, unbound, "project.info", map[string]any{}); err == nil {
		t.Fatal("unbound token without project_id must fail")
	}
}
