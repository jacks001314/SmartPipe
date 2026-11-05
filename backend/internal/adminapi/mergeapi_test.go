package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeVerifier 记录收到的请求，并按 path 返回成功结果。
type fakeVerifier struct {
	mu       sync.Mutex
	paths    []string
	payloads []map[string]any
}

func (f *fakeVerifier) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.payloads = append(f.payloads, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/commit":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "branch": "agent/101", "commit": "abc1234567890def",
				"target_branch": "main", "target_head": "aaa111",
			})
		case "/push":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "branch": "agent/101", "remote": "origin", "after": "abc1234567890def",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeVerifier) last() (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) == 0 {
		return "", nil
	}
	return f.paths[len(f.paths)-1], f.payloads[len(f.payloads)-1]
}

func TestCommitProxiesToVerifierAndRecords(t *testing.T) {
	fv := &fakeVerifier{}
	srv := httptest.NewServer(fv.handler())
	defer srv.Close()

	app, st := newTestAPIWith(t, "", func(c *Config) { c.VerifyURL = srv.URL })
	rec, body := do(t, app, http.MethodPost, "/admin/commits", "", map[string]any{
		"repo":          "/tmp/repo",
		"base_commit":   "deadbeef",
		"patch":         "diff --git a/x b/x\n",
		"issue_ref":     "example/app#101",
		"target_branch": "main",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d: %s", rec.Code, rec.Body.String())
	}
	data := body["data"].(map[string]any)
	if data["ok"] != true || data["branch"] != "agent/101" {
		t.Fatalf("commit data = %+v", data)
	}
	path, payload := fv.last()
	if path != "/commit" {
		t.Fatalf("verifier path = %s", path)
	}
	if payload["branch"] != "agent/101" {
		t.Fatalf("branch not derived from issue: %v", payload["branch"])
	}

	ops, err := st.ListRepoOps(t.Context(), 5)
	if err != nil {
		t.Fatalf("list repo ops: %v", err)
	}
	if len(ops) != 1 || ops[0].Kind != "commit" || !ops[0].OK || ops[0].IssueRef != "example/app#101" {
		t.Fatalf("repo ops = %+v", ops)
	}
	if ops[0].Commit != "abc1234567890def" {
		t.Fatalf("commit sha not stored: %+v", ops[0])
	}
}

func TestPushInjectsCredentialWithoutLeaking(t *testing.T) {
	fv := &fakeVerifier{}
	srv := httptest.NewServer(fv.handler())
	defer srv.Close()

	app, _ := newTestAPIWith(t, "", func(c *Config) { c.VerifyURL = srv.URL })
	rec, body := do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name":            "push-project",
		"github_repo_url": "https://github.com/example/app",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("project status = %d: %s", rec.Code, rec.Body.String())
	}
	projectID := body["data"].(map[string]any)["id"].(string)

	rec, _ = do(t, app, http.MethodPost, "/admin/credentials", "", map[string]any{
		"project_id": projectID, "kind": "github_pat", "name": "pat", "value": "ghp_super_secret_token_value",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("credential status = %d: %s", rec.Code, rec.Body.String())
	}

	rec, body = do(t, app, http.MethodPost, "/admin/pushes", "", map[string]any{
		"project_id": projectID, "repo": "/tmp/repo", "branch": "agent/101", "issue_ref": "example/app#101",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("push status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ghp_super_secret_token_value") {
		t.Fatal("push response leaked the git token")
	}
	path, payload := fv.last()
	if path != "/push" {
		t.Fatalf("verifier path = %s", path)
	}
	remote, _ := payload["remote_url"].(string)
	if !strings.Contains(remote, "x-access-token") || !strings.Contains(remote, "ghp_super_secret_token_value") {
		t.Fatalf("remote_url not credentialed: %s", remote)
	}
	if !strings.Contains(remote, "github.com/example/app") {
		t.Fatalf("remote_url lost repo url: %s", remote)
	}
	if fmt.Sprint(payload["repo"]) != "/tmp/repo" {
		t.Fatalf("repo not forwarded: %v", payload["repo"])
	}
}

func TestPushWithoutCredentialFailsClosed(t *testing.T) {
	fv := &fakeVerifier{}
	srv := httptest.NewServer(fv.handler())
	defer srv.Close()
	app, _ := newTestAPIWith(t, "", func(c *Config) { c.VerifyURL = srv.URL })

	rec, body := do(t, app, http.MethodPost, "/admin/pushes", "", map[string]any{
		"repo": "https://github.com/example/app", "branch": "agent/1",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "git_credential_unavailable" {
		t.Fatalf("error body = %v", body)
	}
	if p, _ := fv.last(); p != "" {
		t.Fatalf("verifier should not be called, got %s", p)
	}
}
