// codex-runner 在一个已检出的工作区里跑一次 Codex 开发任务，输出 unified diff，
// 可选地立刻把这个补丁交给 patch 门禁在干净副本上验证。
//
// 它不带任何 Git 凭据，也不直接提交：提交与推送仍走服务端的 repo.commit /
// repo.push 门禁。这样 Codex 只是“会改代码的执行器”，权限边界不变。
//
// 用法：
//
//	codex-runner -work-dir /srv/ws/issue-42/repo -prompt-file /tmp/task.md \
//	    -out /tmp/42.patch -verify-repo https://github.com/owner/repo -base <commit>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/codexrunner"
	"smartpipe/backend/internal/patch"
)

type report struct {
	OK           bool              `json:"ok"`
	Status       string            `json:"status"`
	TurnID       string            `json:"turn_id,omitempty"`
	ThreadID     string            `json:"thread_id,omitempty"`
	Response     string            `json:"response,omitempty"`
	PatchPath    string            `json:"patch_path"`
	PatchBytes   int               `json:"patch_bytes"`
	ChangedFiles []string          `json:"changed_files,omitempty"`
	DurationMS   int64             `json:"duration_ms"`
	TimedOut     bool              `json:"timed_out"`
	Verify       *patch.Report     `json:"verify,omitempty"`
	Error        string            `json:"error,omitempty"`
	FinishedAt   time.Time         `json:"finished_at"`
	Meta         map[string]string `json:"meta,omitempty"`
}

func main() {
	var (
		workDir    = flag.String("work-dir", "", "已检出的仓库工作区（必填）")
		promptFile = flag.String("prompt-file", "", "任务描述文件；留空则从标准输入读")
		prompt     = flag.String("prompt", "", "任务描述；与 -prompt-file 二选一")
		codexBin   = flag.String("codex", envOr("SMARTPIPE_CODEX_BIN", ""), "codex 可执行文件路径")
		model      = flag.String("model", envOr("SMARTPIPE_CODEX_MODEL", ""), "模型名")
		effort     = flag.String("effort", "", "推理强度 low/medium/high")
		timeout    = flag.Duration("timeout", 30*time.Minute, "整轮执行超时")
		outPath    = flag.String("out", "codex.patch", "补丁输出路径")
		verifyRepo = flag.String("verify-repo", "", "补丁验证用的仓库地址；留空跳过验证")
		baseCommit = flag.String("base", "", "补丁基线 commit；验证时必填")
		issueID    = flag.String("issue", "", "Issue 引用，写入验证报告")
		workRoot   = flag.String("verify-work-root", "", "验证临时目录，留空使用系统临时目录")
		jsonOutput = flag.Bool("json", true, "以 JSON 输出摘要")
	)
	flag.Parse()

	text, err := readPrompt(*prompt, *promptFile)
	if err != nil {
		fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	result, runErr := codexrunner.Run(ctx, codexrunner.Spec{
		WorkDir:      *workDir,
		Prompt:       text,
		CodexPath:    *codexBin,
		Model:        *model,
		Effort:       *effort,
		Timeout:      *timeout,
		LogWriter:    os.Stderr,
		KeepResponse: true,
	})

	rep := report{Status: "ok", PatchPath: *outPath, FinishedAt: time.Now().UTC()}
	if result != nil {
		rep.Status = result.Status
		rep.TurnID = result.TurnID
		rep.ThreadID = result.ThreadID
		rep.Response = result.FinalResponse
		rep.DurationMS = result.DurationMS
		rep.TimedOut = result.TimedOut
	}
	if runErr != nil {
		rep.Status = "error"
		rep.Error = runErr.Error()
	}

	// 任务描述和补丁输出如果落在工作区里，不能混进模型产出。
	diff, diffErr := codexrunner.Diff(ctx, *workDir, "", *promptFile, *outPath)
	if diffErr != nil && rep.Error == "" {
		rep.Error = diffErr.Error()
	}
	if diffErr == nil {
		if err := writePatch(*outPath, diff); err != nil {
			if rep.Error == "" {
				rep.Error = err.Error()
			}
		} else {
			rep.PatchBytes = len(diff)
			if files, err := patch.ChangedFiles(diff); err == nil {
				rep.ChangedFiles = files
			}
		}
	}

	if strings.TrimSpace(*verifyRepo) != "" {
		if strings.TrimSpace(*baseCommit) == "" {
			if rep.Error == "" {
				rep.Error = "-base is required with -verify-repo"
			}
		} else if diff == "" {
			if rep.Error == "" {
				rep.Error = "工作区没有改动，无法验证空补丁"
			}
		} else {
			verifier := &patch.Verifier{
				WorkRoot: *workRoot,
				Limits:   patch.DefaultLimits(),
				DenyPrefixes: []string{
					".git/", ".github/workflows/", "smartpipe.yaml",
				},
			}
			report, err := verifier.Verify(ctx, patch.Bundle{
				IssueID:    *issueID,
				Repo:       *verifyRepo,
				BaseCommit: *baseCommit,
				Patch:      diff,
			}, *verifyRepo)
			if err != nil {
				if rep.Error == "" {
					rep.Error = "verify: " + err.Error()
				}
			} else {
				rep.Verify = report
				if !report.OK && rep.Error == "" {
					rep.Error = "验证未通过：" + report.Error
				}
			}
		}
	}

	rep.OK = rep.Error == "" && rep.PatchBytes > 0 && (rep.Verify == nil || rep.Verify.OK)
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(encoded))
	} else {
		fmt.Printf("status=%s patch=%s bytes=%d ok=%v\n", rep.Status, rep.PatchPath, rep.PatchBytes, rep.OK)
		if rep.Error != "" {
			fmt.Fprintln(os.Stderr, rep.Error)
		}
	}
	if !rep.OK {
		os.Exit(1)
	}
}

func readPrompt(inline, file string) (string, error) {
	if strings.TrimSpace(inline) != "" {
		return inline, nil
	}
	if strings.TrimSpace(file) != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("读取任务描述失败: %w", err)
		}
		return string(raw), nil
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("缺少任务描述：用 -prompt 或 -prompt-file 提供")
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, os.Stdin); err != nil {
		return "", fmt.Errorf("读取标准输入失败: %w", err)
	}
	return buf.String(), nil
}

func writePatch(path, content string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("补丁输出路径为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
