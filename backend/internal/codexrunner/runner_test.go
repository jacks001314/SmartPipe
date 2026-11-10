package codexrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不在 PATH 中")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("git", "add", "-A")
	run.Dir = dir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	commit := exec.Command("git", "commit", "-q", "-m", "base")
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}

func TestSpecValidateRejectsMissingFields(t *testing.T) {
	if _, err := Run(context.Background(), Spec{Prompt: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 work_dir 应返回 ErrInvalid，得到 %v", err)
	}
	if _, err := Run(context.Background(), Spec{WorkDir: t.TempDir()}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 prompt 应返回 ErrInvalid，得到 %v", err)
	}
}

func TestDiffIncludesTrackedAndNewFiles(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\nplus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "new.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := Diff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, want := range []string{"README.md", "src/new.go", "+plus"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("补丁缺少 %q：\n%s", want, diff)
		}
	}
}

func TestDiffHonorsGitignore(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)

	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.tmp"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := Diff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(diff, "node_modules") || strings.Contains(diff, "debug.tmp") {
		t.Fatalf("补丁不应包含被忽略的文件：\n%s", diff)
	}
	if !strings.Contains(diff, "README.md") {
		t.Fatalf("补丁应包含 README.md：\n%s", diff)
	}
}

func TestDiffOnCleanWorkspaceIsEmpty(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)
	diff, err := Diff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.TrimSpace(diff) != "" {
		t.Fatalf("干净工作区应返回空补丁，得到：\n%s", diff)
	}
}

// TestRunLiveCodex 需要本机装有 codex 并已登录；默认跳过。
// 设置 SMARTPIPE_CODEX_LIVE=1 后运行，用最小任务确认 SDK 链路可用。
func TestRunLiveCodex(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SMARTPIPE_CODEX_LIVE")) != "1" {
		t.Skip("设置 SMARTPIPE_CODEX_LIVE=1 才运行真实模型")
	}
	gitAvailable(t)
	dir := initRepo(t)
	result, err := Run(context.Background(), Spec{
		WorkDir:      dir,
		Model:        os.Getenv("SMARTPIPE_CODEX_MODEL"),
		Prompt:       "在 README.md 末尾追加一行：codex-runner smoke。只改这一个文件，不要运行测试，不要提交。",
		Timeout:      5 * time.Minute,
		KeepResponse: true,
		LogWriter:    os.Stderr,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status == "" {
		t.Fatalf("状态为空：%+v", result)
	}
	diff, err := Diff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "README.md") {
		t.Fatalf("模型没有改动 README.md：status=%s response=%q diff=%q", result.Status, result.FinalResponse, diff)
	}
	_ = runtime.GOOS
}
func TestDiffSkipsExcludedPaths(t *testing.T) {
	gitAvailable(t)
	dir := initRepo(t)

	taskPath := filepath.Join(dir, "task.md")
	if err := os.WriteFile(taskPath, []byte("prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := Diff(context.Background(), dir, "", taskPath)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(diff, "task.md") {
		t.Fatalf("被排除的任务描述不应进入补丁：\n%s", diff)
	}
	if !strings.Contains(diff, "README.md") {
		t.Fatalf("补丁应保留 README.md：\n%s", diff)
	}
}

func TestApplyEnvOverridesScrubsSensitiveKeys(t *testing.T) {
	t.Setenv("SMARTPIPE_MASTER_KEY", "secret-value")
	t.Setenv("OPENAI_BASE_URL", "http://old")
	restore := applyEnvOverrides([]string{"OPENAI_BASE_URL=http://new"})
	if got := os.Getenv("SMARTPIPE_MASTER_KEY"); got != "" {
		t.Fatalf("sensitive key leaked to codex env: %q", got)
	}
	if got := os.Getenv("OPENAI_BASE_URL"); got != "http://new" {
		t.Fatalf("override not applied: %q", got)
	}
	restore()
	if got := os.Getenv("SMARTPIPE_MASTER_KEY"); got != "secret-value" {
		t.Fatalf("sensitive key not restored: %q", got)
	}
	if got := os.Getenv("OPENAI_BASE_URL"); got != "http://old" {
		t.Fatalf("override not restored: %q", got)
	}
}
