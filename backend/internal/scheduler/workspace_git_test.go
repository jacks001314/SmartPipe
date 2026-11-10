package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitWorkspacePrepareDiffCleanup(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, src, "init", "-q", "-b", "main")
	runGitTest(t, src, "config", "user.email", "test@example.com")
	runGitTest(t, src, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, src, "add", ".")
	runGitTest(t, src, "commit", "-q", "-m", "init")

	ws := NewGitWorkspace(filepath.Join(root, "wt"), filepath.Join(root, "mirrors"), "git")
	p := &Project{ID: "prj", Name: "demo", Repo: "demo/repo", DefaultBase: "main", LocalPath: src}
	dir, branch, err := ws.Prepare(context.Background(), p, 7, false)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if branch != "agent/issue-7" {
		t.Fatalf("branch=%s", branch)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("worktree content: %q err=%v", data, err)
	}
	// 第二个 Issue 应该是另一个独立目录。
	dir2, _, err := ws.Prepare(context.Background(), p, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	if dir == dir2 {
		t.Fatal("worktrees must be isolated")
	}
	// 修改并产生 diff。
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := diffForTest(dir)
	if err != nil || !strings.Contains(diff, "+world") {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
	if err := ws.Cleanup(context.Background(), p, 7); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree should be removed")
	}
	// mirror 只应有一份
	entries, _ := os.ReadDir(filepath.Join(root, "mirrors"))
	if len(entries) != 1 {
		t.Fatalf("mirror count=%d", len(entries))
	}
}
