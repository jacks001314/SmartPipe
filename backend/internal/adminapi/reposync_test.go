package adminapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSyncLocalCloneFastForwardsFromRemote(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	runGitForTest(t, root, "init", "-q", "--bare", "-b", "main", remote)
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, root, "init", "-q", "-b", "main", seed)
	runGitForTest(t, seed, "config", "user.email", "test@example.com")
	runGitForTest(t, seed, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, seed, "add", ".")
	runGitForTest(t, seed, "commit", "-q", "-m", "v1")
	runGitForTest(t, seed, "remote", "add", "origin", remote)
	runGitForTest(t, seed, "push", "-q", "origin", "main")

	clone := filepath.Join(root, "clone")
	runGitForTest(t, root, "clone", "-q", remote, clone)

	// 远端前进一个提交，clone 还停在旧位置。
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, seed, "commit", "-q", "-am", "v2")
	runGitForTest(t, seed, "push", "-q", "origin", "main")
	wantHead := runGitForTest(t, seed, "rev-parse", "HEAD")

	res, err := syncLocalClone(context.Background(), clone, remote, "", "main")
	if err != nil {
		t.Fatalf("syncLocalClone: %v", err)
	}
	if !res.Updated {
		t.Fatal("expected the clone to move forward")
	}
	if got := runGitForTest(t, clone, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("clone head = %s, want %s", got, wantHead)
	}
	if data, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(data) != "v2\n" {
		t.Fatalf("worktree content = %q, want v2", data)
	}

	// 再同步一次：没有新提交时应报告未更新。
	again, err := syncLocalClone(context.Background(), clone, remote, "", "main")
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if again.Updated {
		t.Fatal("second sync should not report an update")
	}
}

func TestSyncLocalCloneRejectsMissingRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := syncLocalClone(context.Background(), dir, "https://example.invalid/x.git", "", "main"); err == nil {
		t.Fatal("expected an error for a missing local clone")
	}
}
