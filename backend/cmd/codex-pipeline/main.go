// codex-pipeline 把一条 GitHub Issue 走完整个开发链路：
//
//	读 Issue → 准备干净工作区 → Codex 改代码 → 取出补丁
//	→ patch 门禁在干净副本上验证 → 服务端提交 → 推送分支 → 回写 Issue
//
// 它只持有服务地址与各自的管理令牌，不接触 GitHub 与蓝信凭据；
// 提交与推送由 admin-api 完成，模型进程只能改本地文件。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"smartpipe/backend/internal/codexrunner"
)

type issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	URL    string `json:"html_url"`
}

type project struct {
	ID                   string `json:"id"`
	Repo                 string `json:"github_repo_url"`
	IssueRepo            string `json:"github_issue_repo"`
	Default              string `json:"github_default_branch"`
	VerifyPath           string `json:"verify_profile_path"`
	CodexModelProvider   string `json:"codex_model_provider"`
	CodexModel           string `json:"codex_model"`
	CodexReasoningEffort string `json:"codex_reasoning_effort"`
}

// modelDefault 是配置台里的全局默认模型（供应商 + 模型）。
type modelDefault struct {
	ProviderID      string `json:"provider_id"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// providerRuntime 是模型供应商的连接信息，用来给 codex 进程注入环境变量。
type providerRuntime struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	WireAPI string `json:"wire_api"`
	APIKey  string `json:"api_key"`
}

type options struct {
	issueNumber int
	projectID   string
	model       string
	effort      string
	timeoutSet  bool
	codexPath   string
	timeout     time.Duration
	workDir     string
	keep        bool
	apply       bool
	comment     bool
	branch      string
	base        string
	adminURL    string
	adminToken  string
	verifyURL   string
	verifyToken string
	commitRepo  string
}

type summary struct {
	OK         bool             `json:"ok"`
	Stage      string           `json:"stage"`
	Issue      int              `json:"issue"`
	Repo       string           `json:"repo"`
	BaseCommit string           `json:"base_commit"`
	Branch     string           `json:"branch"`
	Commit     string           `json:"commit,omitempty"`
	Status     string           `json:"status,omitempty"`
	Model      string           `json:"model,omitempty"`
	Response   string           `json:"response,omitempty"`
	PatchBytes int              `json:"patch_bytes"`
	Files      []string         `json:"files,omitempty"`
	Verify     json.RawMessage  `json:"verify,omitempty"`
	Commented  bool             `json:"commented"`
	DurationMS int64            `json:"duration_ms"`
	Error      string           `json:"error,omitempty"`
	Steps      []map[string]any `json:"steps,omitempty"`
}

func main() {
	opts := options{}
	flag.IntVar(&opts.issueNumber, "issue", 0, "GitHub Issue 编号（必填）")
	flag.StringVar(&opts.projectID, "project", "", "项目 ID（必填）")
	flag.StringVar(&opts.model, "model", envOr("SMARTPIPE_CODEX_MODEL", ""), "模型名；留空时用项目绑定的模型档案")
	flag.StringVar(&opts.effort, "effort", envOr("SMARTPIPE_CODEX_EFFORT", ""), "推理强度；留空时用项目绑定的模型档案")
	flag.StringVar(&opts.codexPath, "codex", envOr("SMARTPIPE_CODEX_BIN", ""), "codex 可执行文件")
	flag.DurationVar(&opts.timeout, "timeout", 20*time.Minute, "单次执行超时")
	flag.StringVar(&opts.workDir, "work-dir", "", "工作区目录；留空则克隆到临时目录")
	flag.BoolVar(&opts.keep, "keep", false, "结束后保留工作区")
	flag.BoolVar(&opts.apply, "apply", false, "验证通过后真的提交并推送")
	flag.BoolVar(&opts.comment, "comment", true, "把结果回写到 Issue 评论")
	flag.StringVar(&opts.branch, "branch", "", "分支名，默认 agent/issue-<n>")
	flag.StringVar(&opts.base, "base", "", "基线 commit，默认取默认分支 HEAD")
	flag.StringVar(&opts.adminURL, "admin-url", envOr("SMARTPIPE_ADMIN_URL", "http://127.0.0.1:8092"), "admin-api 地址")
	flag.StringVar(&opts.adminToken, "admin-token", os.Getenv("SMARTPIPE_ADMIN_TOKEN"), "admin-api 令牌")
	flag.StringVar(&opts.verifyURL, "verify-url", envOr("SMARTPIPE_VERIFY_URL", "http://127.0.0.1:8095"), "patch-verifier 地址")
	flag.StringVar(&opts.verifyToken, "verify-token", os.Getenv("SMARTPIPE_VERIFY_TOKEN"), "patch-verifier 令牌")
	flag.StringVar(&opts.commitRepo, "commit-repo", "", "提交/推送使用的本地工作副本；默认用本次工作区")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			opts.timeoutSet = true
		}
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	out := run(ctx, opts)
	encoded, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(encoded))
	if !out.OK {
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) *summary {
	started := time.Now()
	out := &summary{Stage: "start", Issue: opts.issueNumber}
	defer func() { out.DurationMS = time.Since(started).Milliseconds() }()
	fail := func(stage string, err error) *summary {
		out.Stage = stage
		out.Error = err.Error()
		out.OK = false
		return out
	}

	if opts.issueNumber <= 0 || strings.TrimSpace(opts.projectID) == "" {
		return fail("input", fmt.Errorf("-issue 与 -project 必填"))
	}
	if err := checkTool("git", opts.adminURL); err != nil {
		return fail("input", err)
	}

	// 1. 读 Issue 与项目配置
	iss, err := fetchIssue(ctx, opts)
	if err != nil {
		return fail("issue", err)
	}
	prj, err := fetchProject(ctx, opts)
	if err != nil {
		return fail("project", err)
	}
	out.Repo = prj.Repo
	// 模型以配置台为准：项目绑定 > 全局默认 > 执行器默认模型。
	if strings.TrimSpace(opts.model) == "" {
		opts.model = strings.TrimSpace(prj.CodexModel)
	}
	if strings.TrimSpace(opts.effort) == "" {
		opts.effort = strings.TrimSpace(prj.CodexReasoningEffort)
	}
	if strings.TrimSpace(opts.model) == "" {
		if def, err := fetchModelDefault(ctx, opts); err == nil {
			if strings.TrimSpace(def.Model) != "" {
				opts.model = def.Model
				if opts.effort == "" {
					opts.effort = strings.TrimSpace(def.ReasoningEffort)
				}
				if strings.TrimSpace(prj.CodexModelProvider) == "" {
					prj.CodexModelProvider = strings.TrimSpace(def.ProviderID)
				}
			}
		}
	}
	if strings.TrimSpace(opts.model) == "" {
		opts.model = "deepseek-v4-flash"
	}
	out.Model = opts.model
	var modelEnv []string
	if providerID := strings.TrimSpace(prj.CodexModelProvider); providerID != "" {
		rt, err := fetchProviderRuntime(ctx, opts, providerID)
		if err != nil {
			return fail("model", err)
		}
		if rt.BaseURL != "" {
			modelEnv = append(modelEnv, "OPENAI_BASE_URL="+rt.BaseURL)
		}
		if rt.APIKey != "" {
			modelEnv = append(modelEnv, "OPENAI_API_KEY="+rt.APIKey)
		}
		out.Steps = append(out.Steps, step("model", map[string]any{
			"provider": rt.Name, "model": opts.model, "wire_api": rt.WireAPI,
		}))
	}
	out.Steps = append(out.Steps, step("issue", map[string]any{"number": iss.Number, "title": iss.Title, "url": iss.URL}))

	// 2. 准备干净工作区
	dir := opts.workDir
	cleanup := func() {}
	if dir == "" {
		dir, err = os.MkdirTemp("", fmt.Sprintf("smartpipe-issue-%d-", opts.issueNumber))
		if err != nil {
			return fail("workspace", err)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	defer func() {
		if opts.keep {
			return
		}
		cleanup()
	}()

	repoDir := filepath.Join(dir, "repo")
	if _, err := os.Stat(repoDir); err != nil {
		if _, err := runGit(ctx, "", "clone", "--quiet", prj.Repo, repoDir); err != nil {
			return fail("workspace", err)
		}
	}
	if opts.base == "" {
		head, err := runGit(ctx, repoDir, "rev-parse", "HEAD")
		if err != nil {
			return fail("workspace", err)
		}
		opts.base = strings.TrimSpace(head)
	} else if _, err := runGit(ctx, repoDir, "checkout", "--quiet", opts.base); err != nil {
		return fail("workspace", err)
	}
	out.BaseCommit = opts.base
	if opts.branch == "" {
		opts.branch = fmt.Sprintf("agent/issue-%d", opts.issueNumber)
	}
	out.Branch = opts.branch
	// admin-api 的 commit/push 需要本地工作副本，而不是仓库 URL。
	if strings.TrimSpace(opts.commitRepo) == "" {
		opts.commitRepo = repoDir
	}
	out.Steps = append(out.Steps, step("workspace", map[string]any{"path": repoDir, "base": opts.base}))

	// 3. Codex 改代码
	prompt := buildPrompt(iss, prj)
	promptPath := filepath.Join(dir, "task.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return fail("prompt", err)
	}
	result, runErr := codexrunner.Run(ctx, codexrunner.Spec{
		WorkDir:      repoDir,
		Prompt:       prompt,
		CodexPath:    opts.codexPath,
		Model:        opts.model,
		Effort:       opts.effort,
		Env:          modelEnv,
		Timeout:      opts.timeout,
		LogWriter:    os.Stderr,
		KeepResponse: true,
	})
	if result != nil {
		out.Status = result.Status
		out.Response = result.FinalResponse
	}
	if runErr != nil {
		return fail("codex", runErr)
	}

	// 4. 取补丁
	diff, err := codexrunner.Diff(ctx, repoDir, "", promptPath)
	if err != nil {
		return fail("diff", err)
	}
	out.PatchBytes = len(diff)
	if strings.TrimSpace(diff) == "" {
		return fail("diff", fmt.Errorf("Codex 没有产生任何文件改动"))
	}
	if files, err := changedFiles(diff); err == nil {
		out.Files = files
	}
	out.Steps = append(out.Steps, step("codex", map[string]any{"status": out.Status, "patch_bytes": out.PatchBytes, "files": out.Files}))

	// 5. 门禁验证
	report, err := verifyPatch(ctx, opts, prj.Repo, diff)
	if err != nil {
		return fail("verify", err)
	}
	out.Verify = report
	var parsed struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(report, &parsed)
	out.Steps = append(out.Steps, step("verify", map[string]any{"ok": parsed.OK, "error": parsed.Error}))
	if !parsed.OK {
		_ = commentIssue(ctx, opts, fmt.Sprintf("## SmartPipe Codex 自动开发\n\n门禁验证未通过，未提交。\n\n```json\n%s\n```", string(report)))
		return fail("verify", fmt.Errorf("门禁验证未通过：%s", parsed.Error))
	}

	if !opts.apply {
		out.OK = true
		out.Stage = "verified"
		return out
	}

	// 6. 提交与推送
	// 服务端会在干净副本上自己应用补丁，所以先把 Codex 留下的改动复位，
	// 否则 repo.commit 会以 worktree is dirty 拒绝。
	if _, err := runGit(ctx, repoDir, "reset", "--hard", opts.base); err != nil {
		return fail("commit", err)
	}
	if _, err := runGit(ctx, repoDir, "clean", "-fd"); err != nil {
		return fail("commit", err)
	}
	commit, err := commitPatch(ctx, opts, opts.commitRepo, diff, iss)
	if err != nil {
		return fail("commit", err)
	}
	out.Commit = commit
	head, err := pushBranch(ctx, opts, opts.commitRepo)
	if err != nil {
		return fail("push", err)
	}
	out.Steps = append(out.Steps, step("commit", map[string]any{"commit": commit, "branch": opts.branch}), step("push", map[string]any{"head": head}))

	// 7. 回写 Issue
	if opts.comment {
		body := fmt.Sprintf("## SmartPipe Codex 自动开发\n\n- 模型：`%s`\n- 分支：`%s`\n- 提交：`%s`\n- 补丁：%d 字节，%d 个文件\n- 门禁：通过\n\n改动文件：\n%s\n\n摘要：%s",
			opts.model, opts.branch, commit, out.PatchBytes, len(out.Files), bulletList(out.Files), out.Response)
		if err := commentIssue(ctx, opts, body); err != nil {
			return fail("comment", err)
		}
		out.Commented = true
	}

	out.OK = true
	out.Stage = "pushed"
	return out
}

func buildPrompt(iss *issue, prj *project) string {
	var b strings.Builder
	b.WriteString("你在一个干净的仓库工作区里修复一个 GitHub Issue。\n\n")
	fmt.Fprintf(&b, "Issue #%d：%s\n\n", iss.Number, iss.Title)
	if strings.TrimSpace(iss.Body) != "" {
		b.WriteString("Issue 正文：\n" + strings.TrimSpace(iss.Body) + "\n\n")
	}
	b.WriteString("要求：\n")
	b.WriteString("1. 只改实现这个 Issue 必需的文件，保持现有代码风格与接口。\n")
	if prj.VerifyPath != "" {
		fmt.Fprintf(&b, "2. 不要修改 %s、.github/ 下的工作流，也不要提交或推送。\n", prj.VerifyPath)
	} else {
		b.WriteString("2. 不要修改 .github/ 下的工作流，也不要提交或推送。\n")
	}
	b.WriteString("3. 改动要能通过项目的构建与单元测试；拿不准时先读 AGENTS.md 与相关代码。\n")
	b.WriteString("4. 完成后用一句话说明改了什么。\n")
	return b.String()
}

func fetchIssue(ctx context.Context, o options) (*issue, error) {
	var out struct {
		Data issue `json:"data"`
	}
	path := fmt.Sprintf("/admin/issues/%d?project_id=%s", o.issueNumber, url.QueryEscape(o.projectID))
	if err := doJSON(ctx, http.MethodGet, o.adminURL+path, o.adminToken, nil, &out); err != nil {
		return nil, fmt.Errorf("读取 Issue 失败: %w", err)
	}
	if out.Data.Number == 0 {
		out.Data.Number = o.issueNumber
	}
	return &out.Data, nil
}

func fetchProject(ctx context.Context, o options) (*project, error) {
	var out struct {
		Data project `json:"data"`
	}
	if err := doJSON(ctx, http.MethodGet, o.adminURL+"/admin/projects/"+url.PathEscape(o.projectID), o.adminToken, nil, &out); err != nil {
		return nil, fmt.Errorf("读取项目失败: %w", err)
	}
	if strings.TrimSpace(out.Data.Repo) == "" {
		return nil, fmt.Errorf("项目 %s 没有配置 github_repo_url", o.projectID)
	}
	if out.Data.Default == "" {
		out.Data.Default = "main"
	}
	return &out.Data, nil
}

func fetchModelDefault(ctx context.Context, o options) (*modelDefault, error) {
	var out struct {
		Data modelDefault `json:"data"`
	}
	if err := doJSON(ctx, http.MethodGet, o.adminURL+"/admin/model-default", o.adminToken, nil, &out); err != nil {
		return nil, fmt.Errorf("读取默认模型失败: %w", err)
	}
	return &out.Data, nil
}

func fetchProviderRuntime(ctx context.Context, o options, id string) (*providerRuntime, error) {
	var out struct {
		Data providerRuntime `json:"data"`
	}
	if err := doJSON(ctx, http.MethodGet, o.adminURL+"/admin/model-providers/"+url.PathEscape(id)+"/runtime", o.adminToken, nil, &out); err != nil {
		return nil, fmt.Errorf("读取模型供应商失败: %w", err)
	}
	return &out.Data, nil
}

func verifyPatch(ctx context.Context, o options, repo, diff string) (json.RawMessage, error) {
	body := map[string]any{
		"repo":        repo,
		"issue_id":    fmt.Sprintf("#%d", o.issueNumber),
		"base_commit": o.base,
		"patch":       diff,
		"author":      "codex",
	}
	req, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(o.verifyURL, "/")+"/verify", bytes.NewReader(req))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.verifyToken != "" {
		httpReq.Header.Set("X-Verify-Token", o.verifyToken)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用 patch-verifier 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("patch-verifier 返回 %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("patch-verifier 返回非 JSON: %s", truncate(string(raw), 300))
	}
	return json.RawMessage(raw), nil
}

func commitPatch(ctx context.Context, o options, repo, diff string, iss *issue) (string, error) {
	body := map[string]any{
		"project_id":   o.projectID,
		"issue_ref":    fmt.Sprintf("#%d", o.issueNumber),
		"repo":         repo,
		"base_commit":  o.base,
		"branch":       o.branch,
		"patch":        diff,
		"message":      fmt.Sprintf("fix(#%d): %s", iss.Number, firstLine(iss.Title)),
		"author_name":  "SmartPipe Codex",
		"author_email": "codex@smartpipe.local",
	}
	var out struct {
		Data struct {
			OK     bool   `json:"ok"`
			Commit string `json:"commit"`
			Error  string `json:"error"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(ctx, http.MethodPost, o.adminURL+"/admin/commits", o.adminToken, body, &out); err != nil {
		return "", err
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("提交被拒绝: %s", out.Error.Message)
	}
	if !out.Data.OK {
		return "", fmt.Errorf("提交失败: %s", out.Data.Error)
	}
	return out.Data.Commit, nil
}

func pushBranch(ctx context.Context, o options, repo string) (string, error) {
	body := map[string]any{
		"project_id": o.projectID,
		"issue_ref":  fmt.Sprintf("#%d", o.issueNumber),
		"repo":       repo,
		"branch":     o.branch,
	}
	var out struct {
		Data struct {
			OK   bool   `json:"ok"`
			Head string `json:"head"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(ctx, http.MethodPost, o.adminURL+"/admin/pushes", o.adminToken, body, &out); err != nil {
		return "", err
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("推送被拒绝: %s", out.Error.Message)
	}
	if !out.Data.OK {
		return "", fmt.Errorf("推送失败")
	}
	return out.Data.Head, nil
}

func commentIssue(ctx context.Context, o options, body string) error {
	payload := map[string]any{"project_id": o.projectID, "body": body}
	path := fmt.Sprintf("/admin/issues/%d/comments", o.issueNumber)
	return doJSON(ctx, http.MethodPost, o.adminURL+path, o.adminToken, payload, nil)
}

func doJSON(ctx context.Context, method, target, token string, payload any, out any) error {
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s 返回 %d: %s", method, target, resp.StatusCode, truncate(string(raw), 400))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), truncate(strings.TrimSpace(stderr.String()), 400))
	}
	return string(out), nil
}

func changedFiles(diff string) ([]string, error) {
	var files []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			files = append(files, strings.TrimPrefix(line, "+++ b/"))
		}
	}
	return files, nil
}

func checkTool(name, _ string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("找不到 %s，请在 PATH 中提供", name)
	}
	return nil
}

func step(name string, detail map[string]any) map[string]any {
	detail["stage"] = name
	return detail
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return "- （无）"
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString("- `" + item + "`\n")
	}
	return b.String()
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
