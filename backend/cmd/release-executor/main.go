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
	// Commands 控制是否长轮询 Agent 发来的遥控命令。
	Commands      bool
	WorkRoot      string
	ExecAllowlist []string
	PathPrepend   []string
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
		ServiceURL:    strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SERVICE_URL")),
		Token:         strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SERVICE_TOKEN")),
		ExecutorID:    strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_ID")),
		OS:            strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_OS")),
		Arch:          strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_ARCH")),
		Commands:      !strings.EqualFold(strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_COMMANDS")), "off"),
		WorkRoot:      strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_WORK_ROOT")),
		ExecAllowlist: splitCommaList(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_EXEC_ALLOWLIST")),
		PathPrepend:   filepath.SplitList(os.Getenv("SMARTPIPE_RELEASE_EXECUTOR_PATH_PREPEND")),
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
	prepareExecutorPath(cfg)
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
	if cfg.Commands && !cfg.Once {
		go runCommandLoop(ctx, cfg)
	}
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

// prepareExecutorPath 把执行器目录下的 bin/ 放到 PATH 最前面。Agent 通过
// executor_exec 安装的工具就落在这里，后续 probe 与构建命令无需改系统环境。
func prepareExecutorPath(cfg config) {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	binDir := filepath.Join(filepath.Dir(exePath), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		log.Printf("create executor bin dir failed: %v", err)
	}
	parts := []string{binDir}
	for _, dir := range cfg.PathPrepend {
		if dir = strings.TrimSpace(dir); dir != "" {
			parts = append(parts, dir)
		}
	}
	// launchd/systemd 拉起执行器时 PATH 往往只有 /usr/bin:/bin，Go、Homebrew
	// 这类常见工具目录不在里面。追加存在的标准目录，构建脚本就不用自己改
	// 环境，也不用把某台机器的路径写进产品配置。
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		"/usr/local/go/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(home, ".local", "bin"),
	} {
		if dir == "" || dir == "/" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			parts = append(parts, dir)
		}
	}
	if current := os.Getenv("PATH"); current != "" {
		parts = append(parts, current)
	}
	if err := os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator))); err != nil {
		log.Printf("prepend executor PATH failed: %v", err)
	}
}

func executeTarget(parent context.Context, client *http.Client, cfg config, env *targetEnvelope) error {
	if env.Target == nil || env.Run == nil {
		return errors.New("invalid target envelope")
	}
	timeoutCtx, cancelTimeout := context.WithTimeout(parent, 3*time.Hour)
	defer cancelTimeout()
	// buildCtx 会被心跳在 run 被取消/判死时中止：取消 run 必须真的停掉构建，
	// 否则执行器主循环被占住，后续 run 的 target 只能一直排队。
	buildCtx, abortBuild := context.WithCancelCause(timeoutCtx)
	defer abortBuild(nil)
	workRoot, err := os.MkdirTemp("", "smartpipe-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workRoot)
	repoDir := filepath.Join(workRoot, "repo")
	output := &logBuffer{max: 2 << 20}
	log.Printf("start target %s run %s commit %s", env.Target.TargetID, env.Run.ID, env.Run.ResolvedCommit)
	// 构建机不再自己 clone：控制面按计划冻结的提交提供源码包，
	// 内网机器只需要能访问 SmartPipe。
	if err := fetchSourceArchive(buildCtx, client, cfg, env, repoDir); err != nil {
		return report(targetResult{err: fmt.Errorf("fetch source archive: %w", err), output: output.String()}, client, cfg, env)
	}
	_, _ = output.Write([]byte("源码取自控制面：" + env.Run.ResolvedCommit + "；顶层：" + describeTopLevel(repoDir) + "\n"))
	stopHeartbeat := startHeartbeat(timeoutCtx, client, cfg, env, abortBuild)
	defer stopHeartbeat()

	sink := newLogSink(client, cfg, env)
	defer sink.Close()
	streamed := io.MultiWriter(output, sink)
	runEnv := []string{
		"SMARTPIPE_RUN_ID=" + env.Run.ID,
		"SMARTPIPE_VERSION=" + env.Run.Version,
		"SMARTPIPE_TAG=" + env.Run.Tag,
		"SMARTPIPE_BUILD_VERSION=" + env.Run.BuildVersion,
		"SMARTPIPE_COMMIT=" + env.Run.ResolvedCommit,
		"SMARTPIPE_CHANNEL=" + env.Run.Channel,
	}

	buildCommand := expandCommand(env.Target.BuildCommand, env.Run)
	if err := runShellEnv(buildCtx, repoDir, buildCommand, runEnv, streamed); err != nil {
		sink.Close()
		if aborted(buildCtx) {
			log.Printf("target %s aborted: %v", env.Target.TargetID, context.Cause(buildCtx))
			return nil
		}
		return report(targetResult{err: fmt.Errorf("build: %w", err), output: output.String()}, client, cfg, env)
	}
	// 签名（codesign / signtool / notarytool 等）放在构建与校验之间：
	// 校验命令可以检查签名产物，上传的制品也已经是签好的。
	if signCommand := expandCommand(env.Target.SignCommand, env.Run); strings.TrimSpace(signCommand) != "" {
		if err := runShellEnv(buildCtx, repoDir, signCommand, runEnv, streamed); err != nil {
			sink.Close()
			if aborted(buildCtx) {
				log.Printf("target %s aborted: %v", env.Target.TargetID, context.Cause(buildCtx))
				return nil
			}
			return report(targetResult{err: fmt.Errorf("sign: %w", err), output: output.String()}, client, cfg, env)
		}
	}
	verifyCommand := expandCommand(env.Target.VerifyCommand, env.Run)
	if strings.TrimSpace(verifyCommand) != "" {
		if err := runShellEnv(buildCtx, repoDir, verifyCommand, runEnv, streamed); err != nil {
			sink.Close()
			if aborted(buildCtx) {
				log.Printf("target %s aborted: %v", env.Target.TargetID, context.Cause(buildCtx))
				return nil
			}
			return report(targetResult{err: fmt.Errorf("verify: %w", err), output: output.String()}, client, cfg, env)
		}
	}
	manifest, err := uploadArtifacts(buildCtx, client, cfg, env, repoDir, streamed)
	if err != nil {
		sink.Close()
		if aborted(buildCtx) {
			log.Printf("target %s aborted: %v", env.Target.TargetID, context.Cause(buildCtx))
			return nil
		}
		// 上传失败通常是对象存储/网络瞬时不可用：标记 retryable，由服务端把目标重排，
		// 而不是把整次发布判死。
		return report(targetResult{err: err, code: "artifact_upload_failed", retryable: true, output: output.String()}, client, cfg, env)
	}
	// 清单是本次构建的权威制品列表：服务端据此合并 run 级清单，下游灌库/
	// 渠道/GitHub 都只认这份数据，不再依赖构建机上的 glob 结果。
	if err := uploadManifest(buildCtx, client, cfg, env, manifest); err != nil {
		sink.Close()
		if aborted(buildCtx) {
			log.Printf("target %s aborted: %v", env.Target.TargetID, context.Cause(buildCtx))
			return nil
		}
		return report(targetResult{err: err, code: "artifact_manifest_failed", retryable: true, output: output.String()}, client, cfg, env)
	}
	sink.Close()
	return report(targetResult{ok: true, output: output.String()}, client, cfg, env)
}

type targetResult struct {
	ok  bool
	err error
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

// errRunNotActive 表示控制面已经不接受这个 target（run 被取消、失败或已收口）。
// 心跳拿到 409 就中止构建，取消才会真正停掉构建进程。
var errRunNotActive = errors.New("release run is no longer active")

// aborted 判断构建是否因 run 不再活跃而中止；这种情况不再向控制面报失败，
// 以免把一个已取消的 run 覆盖成 failed。
func aborted(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errRunNotActive)
}

func startHeartbeat(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope, abortBuild context.CancelCauseFunc) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				body := map[string]any{"run_id": env.Run.ID, "target_id": env.Target.TargetID}
				hbCtx, hbCancel := context.WithTimeout(context.Background(), 10*time.Second)
				status, err := postJSONStatus(hbCtx, client, cfg, "/internal/release-executors/"+cfg.ExecutorID+"/heartbeat", body, nil)
				hbCancel()
				if err != nil {
					if status == http.StatusConflict {
						log.Printf("target %s run %s is no longer active; aborting build", env.Target.TargetID, env.Run.ID)
						abortBuild(errRunNotActive)
						return
					}
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
	status, err := postJSONStatus(ctx, client, cfg, path, body, out)
	if err != nil {
		return false, err
	}
	return status != http.StatusNoContent, nil
}

// postJSONStatus 与 postJSON 相同，但把 HTTP 状态码交给调用方：
// 心跳要靠 409 区分"这个 target 已经不能继续"与普通网络错误。
func postJSONStatus(ctx context.Context, client *http.Client, cfg config, path string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.ServiceURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
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
	return runShellEnv(ctx, dir, command, nil, output)
}

func runShellEnv(ctx context.Context, dir, command string, extraEnv []string, output io.Writer) error {
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
	env := append(os.Environ(), "CI=1", "SMARTPIPE_RELEASE=1")
	env = append(env, extraEnv...)
	cmd.Env = ensureLocaleEnv(env)
	setProcessGroup(cmd)
	return cmd.Run()
}

// defaultUTF8Locale 是非交互 SSH 会话下构建命令使用的回退 locale。
// 构建机通过 SSH 启动时通常没有 LANG/LC_ALL，此时 macOS 上的 makensis
// （NSIS 的 Unicode 构建）会以 std::bad_alloc 崩溃；Linux 上也会出现
// 中文/emoji 文件名的编码问题。构建命令必须有可用的 UTF-8 locale。
func defaultUTF8Locale() string {
	if runtime.GOOS == "darwin" {
		return "en_US.UTF-8"
	}
	return "C.UTF-8"
}

// ensureLocaleEnv 在环境里没有有效 UTF-8 locale 时补一个默认值。
// 显式配了 UTF-8 locale 的调用方保持不变。
func ensureLocaleEnv(env []string) []string {
	if runtime.GOOS == "windows" {
		return env
	}
	if localeIsUTF8(env) {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "LANG=") || strings.HasPrefix(kv, "LC_ALL=") {
			continue
		}
		out = append(out, kv)
	}
	loc := defaultUTF8Locale()
	// LC_ALL 必须一起设置：构建机上常见 LC_CTYPE=C（sshd/launchd 继承），
	// 它会覆盖 LANG，单独的 LANG 不足以让 makensis 走 UTF-8 路径。
	return append(out, "LANG="+loc, "LC_ALL="+loc)
}

func localeIsUTF8(env []string) bool {
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || (name != "LANG" && name != "LC_ALL") {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.Contains(value, "utf-8") || strings.Contains(value, "utf8") {
			return true
		}
	}
	return false
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
	args := []string{"-c", "http.lowSpeedLimit=1024", "-c", "http.lowSpeedTime=20"}
	for _, kv := range cloneEnv {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			return append(args, "-c", "credential.helper=")
		}
	}
	return args
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
