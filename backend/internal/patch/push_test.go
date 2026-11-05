package patch

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPushToBareRemote(t *testing.T) {
	src, base := newRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitTest(t, "", "init", "--bare", "-q", remote)

	patchText := diffOf(t, src, "main.go", "package main\n\nfunc main() { println(\"push\") }\n")
	work := cloneRepo(t, src)
	merged, err := (&Verifier{}).CommitAndMerge(context.Background(), MergeRequest{
		Repo: work, BaseCommit: base, Branch: "agent/issue-7", Patch: patchText,
		Message: "fix(#7): push test",
	})
	if err != nil || !merged.OK {
		t.Fatalf("commit: %+v err=%v", merged, err)
	}
	res, err := (&Verifier{}).Push(context.Background(), PushRequest{
		Repo: work, Branch: "agent/issue-7", RemoteURL: remote,
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !res.OK || res.After != merged.Commit {
		t.Fatalf("result = %+v", res)
	}
	got := runGitTest(t, remote, "rev-parse", "agent/issue-7")
	if got != merged.Commit {
		t.Fatalf("remote ref = %s want %s", got, merged.Commit)
	}
}

func TestPushRejectsNonFastForward(t *testing.T) {
	src, base := newRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitTest(t, "", "init", "--bare", "-q", remote)

	workA := cloneRepo(t, src)
	patchA := diffOf(t, src, "main.go", "package main\n\nfunc main() { println(\"a\") }\n")
	resA, err := (&Verifier{}).CommitAndMerge(context.Background(), MergeRequest{
		Repo: workA, BaseCommit: base, Branch: "agent/issue-8", Patch: patchA,
	})
	if err != nil || !resA.OK {
		t.Fatalf("commit A: %+v err=%v", resA, err)
	}
	if res, err := (&Verifier{}).Push(context.Background(), PushRequest{
		Repo: workA, Branch: "agent/issue-8", RemoteURL: remote,
	}); err != nil || !res.OK {
		t.Fatalf("push A: %+v err=%v", res, err)
	}

	workB := cloneRepo(t, src)
	patchB := diffOf(t, src, "main.go", "package main\n\nfunc main() { println(\"b\") }\n")
	resB, err := (&Verifier{}).CommitAndMerge(context.Background(), MergeRequest{
		Repo: workB, BaseCommit: base, Branch: "agent/issue-8", Patch: patchB,
	})
	if err != nil || !resB.OK {
		t.Fatalf("commit B: %+v err=%v", resB, err)
	}
	res, err := (&Verifier{}).Push(context.Background(), PushRequest{
		Repo: workB, Branch: "agent/issue-8", RemoteURL: remote,
	})
	if err != nil {
		t.Fatalf("push B: %v", err)
	}
	if res.OK || res.Error == "" {
		t.Fatalf("expected non-fast-forward rejection, got %+v", res)
	}
}
