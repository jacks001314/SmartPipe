package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

// 上传失败要带 retryable，服务端才知道把目标退回队列而不是判死。
func TestReportBodyMarksUploadFailureRetryable(t *testing.T) {
	env := &targetEnvelope{
		Run:    &releasev2.Run{ID: "rrun_1"},
		Target: &releasev2.Target{TargetID: "linux-amd64"},
	}
	body := reportBody(targetResult{err: errTest("put: connection refused"), code: "artifact_upload_failed", retryable: true, output: "log"}, env)
	if body["retryable"] != true {
		t.Fatalf("retryable = %v, want true", body["retryable"])
	}
	if body["error_code"] != "artifact_upload_failed" {
		t.Fatalf("error_code = %v", body["error_code"])
	}
	if body["ok"] != false {
		t.Fatalf("ok = %v, want false", body["ok"])
	}
	// 构建失败没有 code 时回落到 target_failed，且不可重试。
	plain := reportBody(targetResult{err: errTest("build: exit status 7")}, env)
	if plain["error_code"] != "target_failed" {
		t.Fatalf("default error_code = %v", plain["error_code"])
	}
	if plain["retryable"] != false {
		t.Fatalf("build failure should not be retryable: %v", plain["retryable"])
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestExpandCommandAndPlatformNormalization(t *testing.T) {
	run := &releasev2.Run{Version: "0.5.16", Tag: "v0.5.16", BuildVersion: "0.5.16.2026-11-12", ResolvedCommit: "abc1234", Channel: "stable"}
	got := expandCommand("build {{version}} {{dated_version}} {{commit}} {{channel}}", run)
	want := "build 0.5.16 0.5.16.2026-11-12 abc1234 stable"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if normalizeOS("win32") != "windows" || normalizeArch("x64") != "amd64" || normalizeArch("aarch64") != "arm64" {
		t.Fatal("platform normalization failed")
	}
}

func TestCredentialEnvName(t *testing.T) {
	cases := map[string]string{
		"git-test":   "SMARTPIPE_CRED_GIT_TEST",
		"GitHub.PAT": "SMARTPIPE_CRED_GITHUB_PAT",
		"a b":        "SMARTPIPE_CRED_A_B",
	}
	for in, want := range cases {
		if got := credentialEnvName(in); got != want {
			t.Fatalf("credentialEnvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildCloneEnv(t *testing.T) {
	dir := t.TempDir()
	env, cleanup, err := buildCloneEnv(dir, "file:///tmp/repo", "git-test")
	if err != nil || env != nil {
		t.Fatalf("local repo should not need credentials: %v %v", env, err)
	}
	cleanup()

	if _, _, err := buildCloneEnv(dir, "https://github.com/acme/private.git", ""); err != nil {
		t.Fatalf("empty ref should be a no-op: %v", err)
	}

	t.Setenv("SMARTPIPE_CRED_GIT_TEST", "")
	if _, _, err := buildCloneEnv(dir, "https://github.com/acme/private.git", "git-test"); err == nil {
		t.Fatal("missing credential should fail loudly")
	}
	t.Setenv("SMARTPIPE_CRED_GIT_TEST", "secret-token")
	env, cleanup, err = buildCloneEnv(dir, "https://github.com/acme/private.git", "git-test")
	if err != nil {
		t.Fatalf("build clone env: %v", err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_ASKPASS=", "SMARTPIPE_GIT_USERNAME=x-access-token", "SMARTPIPE_GIT_PASSWORD=secret-token", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("clone env missing %q: %v", want, env)
		}
	}
	// 非 GitHub 主机可以按凭据引用覆盖用户名（GitLab/Gitea 常见 oauth2/git）。
	t.Setenv("SMARTPIPE_CRED_GIT_TEST_USER", "oauth2")
	env, cleanup, err = buildCloneEnv(dir, "https://gitlab.example.com/acme/private.git", "git-test")
	if err != nil {
		t.Fatalf("build clone env with username override: %v", err)
	}
	if joined := strings.Join(env, "\n"); !strings.Contains(joined, "SMARTPIPE_GIT_USERNAME=oauth2") {
		t.Fatalf("username override not applied: %v", env)
	}
	cleanup()
	if len(gitCloneArgs(env)) == 0 || gitCloneArgs(env)[0] != "-c" {
		t.Fatalf("clone should disable credential helpers when a credential is injected: %v", gitCloneArgs(env))
	}
	cleanup()
	for _, name := range []string{"git-askpass.sh", "git-askpass.cmd"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("askpass script %s should be removed: %v", name, err)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	t.Setenv("SMARTPIPE_CRED_GIT_TEST", "secret-token")
	got := redactSecrets("fatal: auth failed for secret-token")
	if strings.Contains(got, "secret-token") {
		t.Fatalf("token not redacted: %s", got)
	}
}

func TestMergeNoProxyKeepsExistingEntries(t *testing.T) {
	got := mergeNoProxy("corp.internal, 10.0.0.0/8,127.0.0.1")
	for _, want := range []string{"corp.internal", "10.0.0.0/8", "127.0.0.1", "localhost", "::1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("NO_PROXY %q missing %q", got, want)
		}
	}
	if strings.Count(got, "127.0.0.1") != 1 {
		t.Fatalf("duplicate loopback entry: %q", got)
	}
}

func TestGitCloneArgsWithoutCredential(t *testing.T) {
	if args := gitCloneArgs(nil); len(args) != 0 {
		t.Fatalf("no credential should keep default git behavior: %v", args)
	}
}

func TestAskpassAnswer(t *testing.T) {
	t.Setenv("SMARTPIPE_GIT_USERNAME", "oauth2")
	t.Setenv("SMARTPIPE_GIT_PASSWORD", "tok-123")
	if got := askpassAnswer("Username for 'https://gitlab.example.com':"); got != "oauth2" {
		t.Fatalf("username answer = %q", got)
	}
	if got := askpassAnswer("Password for 'https://oauth2@gitlab.example.com':"); got != "tok-123" {
		t.Fatalf("password answer = %q", got)
	}
}
func TestRunShellHandlesNestedQuotes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows cmd quoting regression")
	}
	var out strings.Builder
	err := runShell(context.Background(), t.TempDir(),
		`mkdir dist 2>nul & echo nested> dist\a.txt & powershell -NoProfile -Command "Copy-Item -Path dist\a.txt -Destination dist\b.txt -Force"`, &out)
	if err != nil {
		t.Fatalf("runShell: %v: %s", err, out.String())
	}
	if strings.Contains(out.String(), `-Command "Copy-Item`) {
		t.Fatalf("command was echoed instead of executed: %s", out.String())
	}
}