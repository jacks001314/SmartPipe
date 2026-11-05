package patch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cloneRepo(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clean")
	runGitTest(t, "", "clone", "-q", "--no-hardlinks", src, dst)
	runGitTest(t, dst, "config", "user.email", "lead@example.com")
	runGitTest(t, dst, "config", "user.name", "lead")
	return dst
}

func TestCommitAndMerge(t *testing.T) {
	repo, base := newRepo(t)
	target := runGitTest(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	patchText := diffOf(t, repo, "main.go", "package main\n\nfunc main() { println(\"merged\") }\n")
	work := cloneRepo(t, repo)

	res, err := (&Verifier{}).CommitAndMerge(context.Background(), MergeRequest{
		Repo: work, BaseCommit: base, Branch: "agent/issue-1", Patch: patchText,
		Message: "fix(#1): merged change", AuthorName: "lead", AuthorEmail: "lead@example.com",
		TargetBranch: target,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !res.OK || res.Commit == "" || res.TargetHead == "" || res.TargetHead == res.Commit {
		t.Fatalf("result = %+v", res)
	}
	content, err := os.ReadFile(filepath.Join(work, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "merged") {
		t.Fatalf("merged file = %s", content)
	}
	logOut := runGitTest(t, work, "log", "--oneline", "-3")
	if !strings.Contains(logOut, "fix(#1)") || !strings.Contains(logOut, "Merge branch") {
		t.Fatalf("log = %s", logOut)
	}
}

func TestCommitRejectsDirtyWorktree(t *testing.T) {
	repo, base := newRepo(t)
	patchText := diffOf(t, repo, "main.go", "package main\n")
	work := cloneRepo(t, repo)
	if err := os.WriteFile(filepath.Join(work, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := (&Verifier{}).CommitAndMerge(context.Background(), MergeRequest{
		Repo: work, BaseCommit: base, Branch: "agent/x", Patch: patchText,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "dirty") {
		t.Fatalf("result = %+v", res)
	}
}

func TestCommitRejectsUnsafePatch(t *testing.T) {
	repo, base := newRepo(t)
	work := cloneRepo(t, repo)
	res, err := (&Verifier{}).CommitAndMerge(context.Background(), MergeRequest{
		Repo: work, BaseCommit: base, Branch: "agent/x",
		Patch: "diff --git a/../evil b/../evil\n--- a/../evil\n+++ b/../evil\n@@ -1 +1 @@\n-a\n+b\n",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "path traversal") {
		t.Fatalf("result = %+v", res)
	}
}
