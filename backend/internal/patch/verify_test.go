package patch

import (
	"context"
	"fmt"
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
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGitTest(t, dir, "init", "-q")
	runGitTest(t, dir, "config", "user.email", "dev@example.com")
	runGitTest(t, dir, "config", "user.name", "dev")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := `version: 1
commands:
  build: "git --version"
  unit: "git --version"
required_for_merge:
  - build
  - unit
timeouts:
  build: 30s
`
	if err := os.WriteFile(filepath.Join(dir, "smartpipe.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", ".")
	runGitTest(t, dir, "commit", "-q", "-m", "base")
	return dir, runGitTest(t, dir, "rev-parse", "HEAD")
}

func diffOf(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "diff", "--", file)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	runGitTest(t, dir, "checkout", "--", file)
	// git apply 要求补丁以换行结尾，不能 TrimSpace。
	return string(out)
}

func TestValidateAcceptsNormalPatch(t *testing.T) {
	b := Bundle{BaseCommit: "abcdef1234567", Patch: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"}
	files, err := Validate(b, DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("files = %v", files)
	}
}

func TestValidateRejectsUnsafePatches(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  string
	}{
		{"traversal", "diff --git a/../evil b/../evil\n--- a/../evil\n+++ b/../evil\n@@ -1 +1 @@\n-a\n+b\n", "path traversal"},
		{"workflow", "diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml\n--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n@@ -1 +1 @@\n-a\n+b\n", "denied"},
		{"absolute", "diff --git a//etc/passwd b//etc/passwd\n--- a//etc/passwd\n+++ b//etc/passwd\n@@ -1 +1 @@\n-a\n+b\n", "absolute"},
		{"secret", "diff --git a/config.go b/config.go\n--- a/config.go\n+++ b/config.go\n@@ -1 +1 @@\n-a\n+token := \"ghp_abcdefghijklmnopqrstuvwxyz\"\n", "secret"},
		{"binary", "diff --git a/logo.png b/logo.png\nBinary files a/logo.png and b/logo.png differ\n", "binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(Bundle{BaseCommit: "abcdef1", Patch: tc.patch}, DefaultLimits(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateRejectsTooManyLines(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,501 @@\n")
	for i := 0; i < 501; i++ {
		fmt.Fprintf(&b, "+line %d\n", i)
	}
	_, err := Validate(Bundle{BaseCommit: "abcdef1", Patch: b.String()}, DefaultLimits(), nil)
	if err == nil || !strings.Contains(err.Error(), "changed lines") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifierRunsCleanCopy(t *testing.T) {
	repo, base := newRepo(t)
	patchText := diffOf(t, repo, "main.go", "package main\n\nfunc main() { println(\"ok\") }\n")
	v := &Verifier{}
	report, err := v.Verify(context.Background(), Bundle{
		IssueID: "issue-1", BaseCommit: base, Patch: patchText,
	}, repo)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.OK {
		t.Fatalf("report failed: %s checks=%+v", report.Error, report.Checks)
	}
	if len(report.Files) != 1 || report.Files[0] != "main.go" {
		t.Fatalf("files = %v", report.Files)
	}
	if len(report.Checks) != 2 || !report.Checks[0].OK || !report.Checks[1].OK {
		t.Fatalf("checks = %+v", report.Checks)
	}
	if report.HeadCommit != base {
		t.Fatalf("head = %s want %s", report.HeadCommit, base)
	}
}

func TestVerifierBaseMismatch(t *testing.T) {
	repo, _ := newRepo(t)
	patchText := diffOf(t, repo, "main.go", "package main\n")
	report, err := (&Verifier{}).Verify(context.Background(), Bundle{
		BaseCommit: "0123456", Patch: patchText,
	}, repo)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK || !strings.Contains(report.Error, "base commit") {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifierFailingCommand(t *testing.T) {
	repo, base := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "smartpipe.yaml"), []byte(`version: 1
commands:
  unit: "git --not-a-real-option"
required_for_merge: [unit]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "smartpipe.yaml")
	runGitTest(t, repo, "commit", "-q", "-m", "bad profile")
	base = runGitTest(t, repo, "rev-parse", "HEAD")
	patchText := diffOf(t, repo, "main.go", "package main\n// changed\n")
	report, err := (&Verifier{}).Verify(context.Background(), Bundle{BaseCommit: base, Patch: patchText}, repo)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK || report.Error != "verification command failed" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Checks) != 1 || report.Checks[0].OK {
		t.Fatalf("checks = %+v", report.Checks)
	}
	if report.Checks[0].ExitCode == 0 {
		t.Fatalf("expected non-zero exit: %+v", report.Checks[0])
	}
}

func TestParseProfile(t *testing.T) {
	p, err := ParseProfile([]byte(`version: 1
commands:
  build: "go build ./..."
  unit: "go test ./..."
required_for_merge: [build, unit]
timeouts:
  unit: 5m
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.MergeChecks()) != 2 || p.TimeoutFor("unit").Minutes() != 5 {
		t.Fatalf("profile = %+v", p)
	}
	if _, err := ParseProfile([]byte("version: 1\ncommands:\n  build: \"x\"\nrequired_for_merge: [unit]\n")); err == nil {
		t.Fatal("expected missing required command error")
	}
}

func TestVerifierCreatesWorkRoot(t *testing.T) {
	repo, base := newRepo(t)
	patchText := diffOf(t, repo, "main.go", "package main\n\nfunc main() { println(\"root\") }\n")
	workRoot := filepath.Join(t.TempDir(), "nested", "verify-work")
	report, err := (&Verifier{WorkRoot: workRoot}).Verify(context.Background(), Bundle{
		BaseCommit: base, Patch: patchText,
	}, repo)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.OK {
		t.Fatalf("report failed: %s", report.Error)
	}
	if _, err := os.Stat(workRoot); err != nil {
		t.Fatalf("work root not created: %v", err)
	}
}
