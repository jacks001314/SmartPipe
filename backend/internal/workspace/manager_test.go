package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newSource(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "dev@example.com")
	git(t, dir, "config", "user.name", "dev")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

func TestPrepareAndCleanup(t *testing.T) {
	src, base := newSource(t)
	m := &Manager{Root: filepath.Join(t.TempDir(), "workspaces")}
	info, err := m.Prepare(context.Background(), "issue-1", src, base, "agent/issue-1")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if info.HeadCommit != base || info.Branch != "agent/issue-1" {
		t.Fatalf("info = %+v", info)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Fatalf("workspace missing file: %v", err)
	}
	if got := git(t, info.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "agent/issue-1" {
		t.Fatalf("branch = %s", got)
	}
	list, err := m.List()
	if err != nil || len(list) != 1 || list[0].ID != "issue-1" {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := m.Cleanup("issue-1"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := m.Get("issue-1"); err == nil {
		t.Fatal("meta should be removed")
	}
}

func TestPrepareIdempotentAndConflict(t *testing.T) {
	src, base := newSource(t)
	m := &Manager{Root: filepath.Join(t.TempDir(), "ws")}
	first, err := m.Prepare(context.Background(), "issue-2", src, base, "")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	again, err := m.Prepare(context.Background(), "issue-2", src, base, "")
	if err != nil || again.Path != first.Path {
		t.Fatalf("idempotent: %+v err=%v", again, err)
	}
	git(t, src, "commit", "--allow-empty", "-q", "-m", "next")
	next := git(t, src, "rev-parse", "HEAD")
	if _, err := m.Prepare(context.Background(), "issue-2", src, next, ""); err == nil {
		t.Fatal("expected base conflict")
	}
}

func TestRejectsUnsafeID(t *testing.T) {
	src, base := newSource(t)
	m := &Manager{Root: filepath.Join(t.TempDir(), "ws")}
	for _, id := range []string{"../evil", "..", "a/b", ""} {
		if _, err := m.Prepare(context.Background(), id, src, base, ""); err == nil {
			t.Fatalf("id %q should be rejected", id)
		}
	}
}

func TestShortID(t *testing.T) {
	a := ShortID("example/core#12")
	b := ShortID("example/core#12")
	if a != b || !strings.HasPrefix(a, "ws-") || len(a) != 15 {
		t.Fatalf("short id = %q", a)
	}
	if ShortID("example/core#13") == a {
		t.Fatal("different issues should differ")
	}
}
