package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestHTTPVerifierReadsNestedReport(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "-m", "init")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"record":{"id":5,"ok":true},"report":{"ok":true,"checks":[{"name":"build","ok":true}]},"attempt":null}}`))
	}))
	defer srv.Close()

	v := NewHTTPVerifier(srv.URL, "")
	ok, detail, err := v.Verify(context.Background(), &Project{ID: "prj-1", LocalPath: dir}, dir, "diff --git a/x b/x\n")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatalf("want ok from nested report, got false: %s", detail)
	}
	if !strings.Contains(detail, `"checks"`) || strings.Contains(detail, `"record"`) {
		t.Fatalf("detail should be the inner report, got %s", detail)
	}
}
