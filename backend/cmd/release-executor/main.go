// release-executor pulls release targets from release-service. Build machines
// only make outbound HTTP requests; no inbound port or SSH access is required.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/releasev2"
)

var version = "dev"

type config struct {
	ServiceURL string
	Token      string
	ExecutorID string
	OS         string
	Arch       string
	Labels     []string
	Once       bool
}

type targetEnvelope struct {
	Target *releasev2.Target `json:"target"`
	Run    *releasev2.Run    `json:"run"`
}

func main() {
	// 被 git 当作 GIT_ASKPASS 调用时，只回答一行凭据然后退出。
	if os.Getenv("SMARTPIPE_ASKPASS_MODE") == "1" {
		fmt.Print(askpassAnswer(strings.Join(os.Args[1:], " ")))
		return
	}
	cfg := config{
		ServiceURL: strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SERVICE_URL")),
		Token:      strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SERVICE_TOKEN")),
		ExecutorID: strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_ID")),
		OS:         strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_OS")),
		Arch:       strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_ARCH")),
	}
	flag.StringVar(&cfg.ServiceURL, "service-url", cfg.ServiceURL, "release-service base URL")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "release-service bearer token")
	flag.StringVar(&cfg.ExecutorID, "id", cfg.ExecutorID, "stable executor id")
	flag.StringVar(&cfg.OS, "os", cfg.OS, "executor OS override")
	flag.StringVar(&cfg.Arch, "arch", cfg.Arch, "executor arch override")
	flag.BoolVar(&cfg.Once, "once", false, "claim at most one target and exit")
	flag.Parse()
	if cfg.ServiceURL == "" || cfg.ExecutorID == "" {
		log.Fatal("--service-url 与 --id（或对应环境变量）必填")
	}
	if cfg.OS == "" {
		cfg.OS = normalizeOS(runtime.GOOS)
	}
	if cfg.Arch == "" {
		cfg.Arch = normalizeArch(runtime.GOARCH)
	}
	if err := run(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg config) error {
	client := &http.Client{Timeout: 30 * time.Second}
	executor := releasev2.Executor{
		ID: cfg.ExecutorID, OS: cfg.OS, Arch: cfg.Arch, Version: version, Status: "online",
		Labels: cfg.Labels, Capabilities: map[string]any{"runtime": runtime.Version(), "pid": os.Getpid()},
	}
	var regErr error
	for attempt := 0; attempt < 12; attempt++ {
		if regErr = postJSON(ctx, client, cfg, "/internal/release-executors/register", executor, nil); regErr == nil {
			break
		}
		log.Printf("register executor attempt %d failed: %v", attempt+1, regErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if regErr != nil {
		return fmt.Errorf("register executor: %w", regErr)
	}
	log.Printf("release executor %s (%s/%s) registered at %s", cfg.ExecutorID, cfg.OS, cfg.Arch, cfg.ServiceURL)
	registerEvery := time.NewTicker(30 * time.Second)
	defer registerEvery.Stop()
	pollEvery := time.NewTicker(2 * time.Second)
	defer pollEvery.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-registerEvery.C:
			if err := postJSON(ctx, client, cfg, "/internal/release-executors/register", executor, nil); err != nil {
				log.Printf("heartbeat/register failed: %v", err)
			}
		case <-pollEvery.C:
			var env targetEnvelope
			found, err := postJSONMaybeNoContent(ctx, client, cfg, "/internal/release-executors/"+cfg.ExecutorID+"/claim", map[string]any{}, &env)
			if err != nil {
				log.Printf("claim failed: %v", err)
				continue
			}
			if !found {
				continue
			}
			if err := executeTarget(ctx, client, cfg, &env); err != nil {
				log.Printf("target %s/%s failed: %v", env.Run.ID, env.Target.TargetID, err)
			}
			if cfg.Once {
				return nil
			}
		}
	}
}

func executeTarget(parent context.Context, client *http.Client, cfg config, env *targetEnvelope) error {
	if env.Target == nil || env.Run == nil {
		return errors.New("invalid target envelope")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Hour)
	defer cancel()
	workRoot, err := os.MkdirTemp("", "smartpipe-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workRoot)
	repoDir := filepath.Join(workRoot, "repo")
	output := &logBuffer{max: 2 << 20}
	log.Printf("start target %s run %s commit %s", env.Target.TargetID, env.Run.ID, env.Run.ResolvedCommit)
	cloneEnv, cleanupCred, err := buildCloneEnv(workRoot, env.Run.Repo, env.Run.CloneCredential)
	if err != nil {
		return report(targetResult{err: err, output: output.String()}, client, cfg, env)
	}
	defer cleanupCred()
	cloneArgs := append(gitCloneArgs(cloneEnv), "clone", "--no-hardlinks", "--quiet", env.Run.Repo, repoDir)
	if err := runCommandEnv(ctx, "", cloneEnv, "git", cloneArgs...); err != nil {
		return report(targetResult{err: err, output: output.String()}, client, cfg, env)
	}
	if err := runCommandEnv(ctx, repoDir, cloneEnv, "git", "checkout", "--detach", env.Run.ResolvedCommit); err != nil {
		return report(targetResult{err: err, output: output.String()}, client, cfg, env)
	}
	stopHeartbeat := startHeartbeat(ctx, client, cfg, env)
	defer stopHeartbeat()

	sink := newLogSink(client, cfg, env)
	defer sink.Close()
	streamed := io.MultiWriter(output, sink)

	buildCommand := expandCommand(env.Target.BuildCommand, env.Run)
	if err := runShell(ctx, repoDir, buildCommand, streamed); err != nil {
		sink.Close()
		return report(targetResult{err: fmt.Errorf("build: %w", err), output: output.String()}, client, cfg, env)
	}
	// 签名（codesign / signtool / notarytool 等）放在构建与校验之间：
	// 校验命令可以检查签名产物，上传的制品也已经是签好的。
	if signCommand := expandCommand(env.Target.SignCommand, env.Run); strings.TrimSpace(signCommand) != "" {
		if err := runShell(ctx, repoDir, signCommand, streamed); err != nil {
			sink.Close()
			return report(targetResult{err: fmt.Errorf("sign: %w", err), output: output.String()}, client, cfg, env)
		}
	}
	verifyCommand := expandCommand(env.Target.VerifyCommand, env.Run)
	if err := runShell(ctx, repoDir, verifyCommand, streamed); err != nil {
		sink.Close()
		return report(targetResult{err: fmt.Errorf("verify: %w", err), output: output.String()}, client, cfg, env)
	}
	if err := uploadArtifacts(ctx, client, cfg, env, repoDir, streamed); err != nil {
		sink.Close()
		// 上传失败通常是对象存储/网络瞬时不可用：标记 retryable，由服务端把目标重排，
		// 而不是把整次发布判死。
		return report(targetResult{err: err, code: "artifact_upload_failed", retryable: true, output: output.String()}, client, cfg, env)
	}
	sink.Close()
	return report(targetResult{ok: true, output: output.String()}, client, cfg, env)
}

type targetResult struct {
	ok     bool
	err    error
	// code 覆盖默认的 target_failed，便于区分失败类别。
	code string
	// retryable 表示这次失败可以重试（服务端会把目标退回队列）。
	retryable bool
	output    string
}

// reportBody 把执行结果整理成 /complete 的请求体，失败时带上错误码与是否可重试。
func reportBody(result targetResult, env *targetEnvelope) map[string]any {
	body := map[string]any{
		"run_id": env.Run.ID, "target_id": env.Target.TargetID, "ok": result.ok,
		"log": result.output,
	}
	if result.err != nil {
		code := result.code
		if code == "" {
			code = "target_failed"
		}
		body["error_code"] = code
		body["error_message"] = result.err.Error()
		body["retryable"] = result.retryable
	}
	return body
}

func report(result targetResult, client *http.Client, cfg config, env *targetEnvelope) error {
	body := reportBody(result, env)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return postJSON(ctx, client, cfg, "/internal/release-executors/"+cfg.ExecutorID+"/complete", body, nil)
}

func startHeartbeat(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				body := map[string]any{"run_id": env.Run.ID, "target_id": env.Target.TargetID}
				hbCtx, hbCancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := postJSON(hbCtx, client, cfg, "/internal/release-executors/"+cfg.ExecutorID+"/heartbeat", body, nil)
				hbCancel()
				if err != nil {
					log.Printf("target heartbeat failed: %v", err)
				}
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

func postJSON(ctx context.Context, client *http.Client, cfg config, path string, body, out any) error {
	_, err := postJSONMaybeNoContent(ctx, client, cfg, path, body, out)
	return err
}

func postJSONMaybeNoContent(ctx context.Context, client *http.Client, cfg config, path string, body, out any) (bool, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.ServiceURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return false, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return false, err
		}
	}
	return true, nil
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.buf.Len() > b.max {
		raw := b.buf.Bytes()
		keep := append([]byte(nil), raw[len(raw)-b.max:]...)
		b.buf.Reset()
		_, _ = b.buf.Write([]byte("...[truncated]\n"))
		_, _ = b.buf.Write(keep)
	}
	return n, err
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runShell(ctx context.Context, dir, command string, output io.Writer) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("empty command")
	}
	var cmd *exec.Cmd
	cleanup := func() {}
	if runtime.GOOS == "windows" {
		// Windows 走临时 .cmd 文件：Go 的 os/exec 对 cmd /C 的内嵌引号转义与 cmd 语义不一致，
		// 直接把含引号的命令当参数传会让 PowerShell 把命令原样打印而不执行。
		file, err := os.CreateTemp("", "smartpipe-cmd-*.cmd")
		if err != nil {
			return fmt.Errorf("prepare command script: %w", err)
		}
		body := strings.ReplaceAll(strings.ReplaceAll(command, "\r\n", "\n"), "\n", "\r\n")
		if _, err := file.WriteString("@echo off\r\n" + body + "\r\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
			return fmt.Errorf("write command script: %w", err)
		}
		_ = file.Close()
		cmd = exec.CommandContext(ctx, "cmd", "/C", file.Name())
		cleanup = func() { _ = os.Remove(file.Name()) }
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	defer cleanup()
	cmd.Dir = dir
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(), "CI=1", "SMARTPIPE_RELEASE=1")
	return cmd.Run()
}

func runCommand(ctx context.Context, dir, name string, args ...string) error {
	return runCommandEnv(ctx, dir, nil, name, args...)
}

func runCommandEnv(ctx context.Context, dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, redactSecrets(strings.TrimSpace(string(raw))))
	}
	return nil
}

// buildCloneEnv 为私有仓库准备 git 凭据。令牌只通过 askpass 脚本的环境变量传给 git，
// 不出现在命令行参数里（避免 ps 泄漏），也不会写进构建日志。
func buildCloneEnv(workRoot, repo, credentialRef string) ([]string, func(), error) {
	ref := strings.TrimSpace(credentialRef)
	if ref == "" || !strings.HasPrefix(strings.ToLower(repo), "http") {
		return nil, func() {}, nil
	}
	token, err := resolveCredential(ref)
	if err != nil {
		return nil, func() {}, err
	}
	user := gitUsernameFor(repo)
	if override := strings.TrimSpace(os.Getenv(credentialEnvName(ref) + "_USER")); override != "" {
		user = override
	}
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"SMARTPIPE_GIT_USERNAME=" + user,
		"SMARTPIPE_GIT_PASSWORD=" + token,
	}
	cleanup := func() {}
	if runtime.GOOS == "windows" {
		// Windows 不能把 .sh/.cmd 直接交给 CreateProcess；让执行器自己进入 askpass 模式回答提示，
		// 令牌仍然只通过环境变量传递，不进命令行。
		exe, err := os.Executable()
		if err != nil {
			return nil, cleanup, fmt.Errorf("locate askpass executable: %w", err)
		}
		env = append(env, "GIT_ASKPASS="+exe, "SMARTPIPE_ASKPASS_MODE=1")
	} else {
		script := filepath.Join(workRoot, "git-askpass.sh")
		if err := os.WriteFile(script, []byte(gitAskpassScript), 0o700); err != nil {
			return nil, cleanup, fmt.Errorf("write askpass: %w", err)
		}
		env = append(env, "GIT_ASKPASS="+script)
		cleanup = func() { _ = os.Remove(script) }
	}
	// 构建机上常配全局代理；内网/回环仓库必须绕过，否则 git 会把 127.0.0.1 也交给代理。
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		env = append(env, key+"="+mergeNoProxy(os.Getenv(key)))
	}
	return env, cleanup, nil
}

// askpassAnswer 按 git 的提示返回用户名或密码，供 Windows 的自身 askpass 模式使用。
func askpassAnswer(prompt string) string {
	if strings.Contains(strings.ToLower(prompt), "username") {
		return os.Getenv("SMARTPIPE_GIT_USERNAME")
	}
	return os.Getenv("SMARTPIPE_GIT_PASSWORD")
}

// gitAskpassScript 让 git 用环境变量里的令牌回答用户名/密码提示；令牌不落盘。
const gitAskpassScript = `#!/bin/sh
case "$1" in
  *[Uu]sername*) printf '%s' "$SMARTPIPE_GIT_USERNAME" ;;
  *) printf '%s' "$SMARTPIPE_GIT_PASSWORD" ;;
esac
`

// mergeNoProxy 把回环地址并入现有 NO_PROXY，保留用户原来的网段配置。
func mergeNoProxy(existing string) string {
	want := []string{"127.0.0.1", "localhost", "::1"}
	seen := map[string]bool{}
	parts := make([]string, 0, len(want)+4)
	for _, item := range strings.Split(existing, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		parts = append(parts, item)
	}
	for _, item := range want {
		if !seen[item] {
			parts = append(parts, item)
		}
	}
	return strings.Join(parts, ",")
}

// gitCloneArgs 注入凭据时禁用系统凭据助手：Windows 的 GCM 会抢走 askpass 并弹出登录窗口。
func gitCloneArgs(cloneEnv []string) []string {
	for _, kv := range cloneEnv {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			return []string{"-c", "credential.helper="}
		}
	}
	return nil
}

// credentialEnvName 把引用名转成构建机上的环境变量名（与 releasepublish 的约定一致）。
func credentialEnvName(ref string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(ref) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return "SMARTPIPE_CRED_" + strings.ToUpper(b.String())
}

func resolveCredential(ref string) (string, error) {
	value := strings.TrimSpace(os.Getenv(credentialEnvName(ref)))
	if value == "" {
		return "", fmt.Errorf("clone credential %q is not configured on this build machine (%s)", ref, credentialEnvName(ref))
	}
	return value, nil
}

// gitUsernameFor 让 GitHub 用 x-access-token 形式做 Basic 认证；其它主机退回 git。
func gitUsernameFor(repo string) string {
	if strings.Contains(strings.ToLower(repo), "github.com") {
		return "x-access-token"
	}
	return "git"
}

// redactSecrets 兜底擦掉构建机环境里配置过的仓库令牌，避免 git 报错时带出来。
func redactSecrets(text string) string {
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, "SMARTPIPE_CRED_") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 8 {
			text = strings.ReplaceAll(text, value, "***")
		}
	}
	return text
}

func expandCommand(command string, run *releasev2.Run) string {
	replacer := strings.NewReplacer(
		"{{version}}", run.Version,
		"{{tag}}", run.Tag,
		"{{build_version}}", run.BuildVersion,
		"{{dated_version}}", run.BuildVersion,
		"{{commit}}", run.ResolvedCommit,
		"{{channel}}", run.Channel,
	)
	return replacer.Replace(command)
}

func normalizeOS(osName string) string {
	switch strings.ToLower(osName) {
	case "windows", "win32":
		return "windows"
	case "darwin":
		return "darwin"
	case "linux":
		return "linux"
	default:
		return strings.ToLower(osName)
	}
}

func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "amd64", "x64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return strings.ToLower(arch)
	}
}

// logSink streams build output in small chunks so the release center can show
// progress while a long build is still running.
type logSink struct {
	mu      sync.Mutex
	pending strings.Builder
	seq     int
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	client  *http.Client
	cfg     config
	env     *targetEnvelope
}

func newLogSink(client *http.Client, cfg config, env *targetEnvelope) *logSink {
	ctx, cancel := context.WithCancel(context.Background())
	l := &logSink{ctx: ctx, cancel: cancel, client: client, cfg: cfg, env: env}
	go l.loop()
	return l
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return len(p), nil
	}
	l.pending.Write(p)
	return len(p), nil
}

func (l *logSink) loop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.flush()
		}
	}
}

func (l *logSink) flush() {
	for {
		l.mu.Lock()
		raw := l.pending.String()
		if raw == "" {
			l.mu.Unlock()
			return
		}
		chunk := raw
		if len(chunk) > 16<<10 {
			chunk = chunk[:16<<10]
		}
		l.pending.Reset()
		l.pending.WriteString(raw[len(chunk):])
		l.seq++
		seq := l.seq
		l.mu.Unlock()
		body := map[string]any{
			"run_id": l.env.Run.ID, "target_id": l.env.Target.TargetID, "chunk": chunk, "seq": seq,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := postJSON(ctx, l.client, l.cfg, "/internal/release-executors/"+l.cfg.ExecutorID+"/log", body, nil)
		cancel()
		if err != nil {
			log.Printf("log upload failed: %v", err)
			return
		}
	}
}

func (l *logSink) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	l.flush()
	l.cancel()
}
