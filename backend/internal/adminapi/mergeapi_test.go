package adminapi

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mergeFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "absent"))
	repo := t.TempDir()
	fixtureGit(t, repo, "init", "-q", "-b", "main")
	fixtureGit(t, repo, "config", "user.name", "test")
	fixtureGit(t, repo, "config", "user.email", "test@example.invalid")
	fixtureGit(t, repo, "config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(repo, "x.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "x.txt")
	fixtureGit(t, repo, "commit", "-q", "-m", "base")
	return repo, fixtureGit(t, repo, "rev-parse", "HEAD")
}

func TestCommitLocallyAndRecords(t *testing.T) {
	app, st := newTestAPI(t, "")
	repo, base := mergeFixture(t)
	rec, body := do(t, app, http.MethodPost, "/admin/commits", "", map[string]any{
		"repo": repo, "base_commit": base, "issue_ref": "example/app#101", "target_branch": "main",
		"patch": "diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-before\n+after\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	data := body["data"].(map[string]any)
	sha := fixtureGit(t, repo, "rev-parse", "agent/101")
	if data["ok"] != true || data["branch"] != "agent/101" || data["commit"] != sha {
		t.Fatalf("data: %+v", data)
	}
	if fixtureGit(t, repo, "show", "main:x.txt") != "after" {
		t.Fatal("patch not merged to main")
	}
	ops, err := st.ListRepoOps(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Kind != "commit" || !ops[0].OK || ops[0].Commit != sha || ops[0].IssueRef != "example/app#101" {
		t.Fatalf("ops: %+v", ops)
	}
}

func TestPushInjectsCredentialWithoutLeaking(t *testing.T) {
	app, st := newTestAPI(t, "")
	rec, body := do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name": "push-project", "github_repo_url": "https://github.com/example/app",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("project: %s", rec.Body.String())
	}
	id := body["data"].(map[string]any)["id"].(string)
	const token = "test-only-fake-token"
	rec, _ = do(t, app, http.MethodPost, "/admin/credentials", "", map[string]any{
		"project_id": id, "kind": "github_pat", "name": "test", "value": token,
	})
	if rec.Code != http.StatusCreated {
		t.Fatal("credential fixture failed")
	}
	project, err := st.GetProject(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := (&API{store: st}).resolvePushRemote(t.Context(), project, "/unused")
	if err != nil {
		t.Fatal(err)
	}
	if remote != "https://x-access-token:"+token+"@github.com/example/app" {
		t.Fatal("credential injection mismatch")
	}
	repo, sha := mergeFixture(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	fixtureGit(t, repo, "init", "--bare", "-q", bare)
	fixtureGit(t, repo, "branch", "agent/101")
	if _, err := st.DB().Exec("UPDATE projects SET github_repo_url=$1 WHERE id=$2", bare, id); err != nil {
		t.Fatal(err)
	}
	rec, body = do(t, app, http.MethodPost, "/admin/pushes", "", map[string]any{
		"project_id": id, "repo": repo, "branch": "agent/101",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatal("token leaked")
	}
	if body["data"].(map[string]any)["ok"] != true {
		t.Fatal("push not successful")
	}
	if fixtureGit(t, repo, "--git-dir="+bare, "rev-parse", "refs/heads/agent/101") != sha {
		t.Fatal("wrong remote commit")
	}
	ops, err := st.ListRepoOps(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || !ops[0].OK || ops[0].Kind != "push" || ops[0].Commit != sha {
		t.Fatalf("ops: %+v", ops)
	}
}

func TestPushWithoutCredentialFailsClosed(t *testing.T) {
	app, _ := newTestAPI(t, "")
	rec, body := do(t, app, http.MethodPost, "/admin/pushes", "", map[string]any{
		"repo": "https://github.com/example/app", "branch": "agent/1",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	if body["error"].(map[string]any)["code"] != "git_credential_unavailable" {
		t.Fatalf("body: %+v", body)
	}
}
