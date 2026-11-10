package patch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseRefSHAIgnoresWarnings(t *testing.T) {
	out := "warning: redirecting to https://github.com/example/repo.git/\n" +
		"e89c891dd5132fffba1db2822a65c32cd6a5cee4\trefs/heads/agent/issue-7\n"
	if got := parseRefSHA(out); got != "e89c891dd5132fffba1db2822a65c32cd6a5cee4" {
		t.Fatalf("unexpected sha: %q", got)
	}
	if got := parseRefSHA("warning: only\n"); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestPushForceWithLeaseOverwritesStaleBranch(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	mustGit := func(wd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	mustGit("", "init", "--bare", "-q", remote)
	mustGit("", "init", "-q", "-b", "main", work)
	mustGit(work, "config", "user.name", "t")
	mustGit(work, "config", "user.email", "t@example.com")
	commit := func(body string) {
		if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(work, "add", "-A")
		mustGit(work, "commit", "-q", "-m", "c-"+body)
	}
	// A：公共基线
	commit("base")
	mustGit(work, "push", "-q", remote, "main:refs/heads/main")

	// 远端分支上是失败尝试留下的提交
	mustGit(work, "checkout", "-q", "-b", "agent/issue-1")
	commit("remote-attempt")
	mustGit(work, "push", "-q", remote, "agent/issue-1")

	// 本地回到基线后产生一个兄弟提交，与远端分支不是快进关系
	mustGit(work, "checkout", "-q", "main")
	commit("local-retry")
	mustGit(work, "branch", "-f", "agent/issue-1")

	v := &Verifier{}
	req := PushRequest{Repo: work, Branch: "agent/issue-1", RemoteURL: remote}
	res, err := v.Push(context.Background(), req)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.OK {
		t.Fatal("diverged branch should be rejected without force")
	}
	req.ForceWithLease = true
	res, err = v.Push(context.Background(), req)
	if err != nil {
		t.Fatalf("force push: %v", err)
	}
	if !res.OK {
		t.Fatalf("force-with-lease push failed: %s", res.Error)
	}
}
