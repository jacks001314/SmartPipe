package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCommitterCommitsWorktreeChanges(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "agent/issue-1", dir},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := GitCommitter{GitBin: "git"}
	sha, err := c.Commit(context.Background(), dir, "fix: demo (#1)")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("bad sha %q", sha)
	}
	cmd := exec.Command("git", "-C", dir, "log", "-1", "--format=%s%n%an")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("log: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "fix: demo (#1)") || !strings.Contains(string(out), "SmartPipe Codex") {
		t.Fatalf("unexpected log: %s", out)
	}
	if _, err := c.Commit(context.Background(), dir, "empty"); err == nil {
		t.Fatal("commit without changes should fail")
	}
}
