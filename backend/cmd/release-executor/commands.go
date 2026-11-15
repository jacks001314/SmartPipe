package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"smartpipe/backend/internal/releasev2"
)

const maxCommandOutputBytes = 128 << 10

type executorCommandResult struct {
	exitCode     int
	output       string
	errorMessage string
}

// runCommandLoop 长轮询 release-service 的命令队列，让 Agent 能远程探测和
// 修复构建机环境。构建机仍然只发起出站请求，不需要暴露端口。
func runCommandLoop(ctx context.Context, cfg config) {
	pollClient := &http.Client{Timeout: 45 * time.Second}
	for {
		if ctx.Err() != nil {
			return
		}
		cmd, err := nextExecutorCommand(ctx, pollClient, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logf := fmt.Sprintf("executor command poll failed: %v", err)
			fmt.Fprintln(os.Stderr, logf)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if cmd == nil {
			continue
		}
		if !commandAllowed(cfg, cmd) {
			_ = reportExecutorCommand(ctx, cfg, cmd.ID, executorCommandResult{
				exitCode: 126, errorMessage: "command is not allowed by the executor allowlist",
			})
			continue
		}
		result := executeExecutorCommand(ctx, cfg, cmd)
		if err := reportExecutorCommand(ctx, cfg, cmd.ID, result); err != nil {
			fmt.Fprintf(os.Stderr, "report executor command %d failed: %v\n", cmd.ID, err)
		}
	}
}

func nextExecutorCommand(ctx context.Context, client *http.Client, cfg config) (*releasev2.ExecutorCommand, error) {
	endpoint := strings.TrimRight(cfg.ServiceURL, "/") + "/internal/release-executors/" +
		url.PathEscape(cfg.ExecutorID) + "/commands/next?wait_seconds=25"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var cmd releasev2.ExecutorCommand
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return nil, err
	}
	return &cmd, nil
}

func reportExecutorCommand(ctx context.Context, cfg config, id int64, result executorCommandResult) error {
	body := map[string]any{
		"exit_code": result.exitCode, "output": result.output, "error_message": result.errorMessage,
	}
	reportCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	return postJSON(reportCtx, client, cfg,
		"/internal/release-executors/"+url.PathEscape(cfg.ExecutorID)+"/commands/"+strconv.FormatInt(id, 10)+"/result", body, nil)
}

func executeExecutorCommand(ctx context.Context, cfg config, cmd *releasev2.ExecutorCommand) executorCommandResult {
	switch cmd.Kind {
	case releasev2.ExecutorCommandProbe:
		return probeBuildHost(ctx, cfg)
	case releasev2.ExecutorCommandExec:
		return execBuildCommand(ctx, cfg, cmd)
	case releasev2.ExecutorCommandLogs:
		return commandLogs(cfg, cmd)
	case releasev2.ExecutorCommandFiles:
		return commandFiles(cfg, cmd)
	default:
		return executorCommandResult{exitCode: 2, errorMessage: "unknown command kind " + string(cmd.Kind)}
	}
}

func commandWorkRoot(cfg config) string {
	if root := strings.TrimSpace(cfg.WorkRoot); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, ".smartpipe", "release-executor", "work")
}

func resolveCommandPath(cfg config, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	return filepath.Join(commandWorkRoot(cfg), raw), nil
}

func intPayload(cmd *releasev2.ExecutorCommand, key string, fallback, max int) int {
	value, ok := cmd.Payload[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		fallback = int(typed)
	case int:
		fallback = typed
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			fallback = parsed
		}
	}
	if fallback < 0 {
		fallback = 0
	}
	if max > 0 && fallback > max {
		fallback = max
	}
	return fallback
}

func stringPayload(cmd *releasev2.ExecutorCommand, key string) string {
	value, _ := cmd.Payload[key].(string)
	return strings.TrimSpace(value)
}

// commandAllowed 对 exec 做首词白名单检查；probe/logs/files 由服务端和
// 参数校验约束，不需要 shell 白名单。
func commandAllowed(cfg config, cmd *releasev2.ExecutorCommand) bool {
	if cmd.Kind != releasev2.ExecutorCommandExec {
		return true
	}
	allow := cfg.ExecAllowlist
	if len(allow) == 0 {
		allow = defaultExecAllowlist
	}
	allowed := map[string]bool{}
	for _, item := range allow {
		allowed[strings.ToLower(strings.TrimSpace(item))] = true
	}
	for _, segment := range splitCommandSegments(cmd.Command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		// 命令里常用引号包裹可执行文件路径；去掉引号后再取 basename，
		// 否则 `"$HOME/bin/node" -v` 会被误判为不在白名单。
		token := strings.ToLower(filepath.Base(strings.Trim(fields[0], `"'\`)))
		if token == "sudo" {
			return false
		}
		if !allowed[token] {
			return false
		}
	}
	return true
}

func splitCommandSegments(command string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	return strings.Split(replacer.Replace(command), "\n")
}

var defaultExecAllowlist = []string{
	"node", "npm", "npx", "pnpm", "yarn", "corepack",
	"go", "git", "xcodebuild", "xcrun", "codesign", "hdiutil", "security", "xcode-select",
	"brew", "port", "sw_vers", "uname", "which", "env", "printenv",
	"cd", "export", "set", "unset", "true", "false", "test", "echo", "printf",
	"ls", "du", "df", "mkdir", "rmdir", "cp", "mv", "rm", "chmod", "chown", "touch", "ln",
	"cat", "tail", "head", "grep", "sed", "awk", "find", "wc", "sort", "uniq",
	"tar", "zip", "unzip", "curl", "shasum", "openssl", "stat", "pwd", "date", "sleep",
	"tee", "xargs", "dirname", "basename", "realpath", "readlink",
	// macOS 诊断与包管理：Agent 定位 dyld/架构/签名问题时需要这些只读工具；
	// launchctl 用来重启执行器本身（构建机环境修好后让新 PATH 生效）。
	"file", "otool", "lipo", "plutil", "pkgutil", "spctl", "xattr", "ditto",
	"install_name_tool", "launchctl",
}

func splitCommaList(raw string) []string {
	out := []string{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func execBuildCommand(ctx context.Context, cfg config, cmd *releasev2.ExecutorCommand) executorCommandResult {
	cwd := strings.TrimSpace(cmd.Cwd)
	if cwd == "" {
		cwd = commandWorkRoot(cfg)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return executorCommandResult{exitCode: 1, errorMessage: "cwd: " + err.Error()}
	}
	timeout := intPayload(cmd, "timeout_seconds", 600, 1800)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var shell *exec.Cmd
	if runtime.GOOS == "windows" {
		shell = exec.CommandContext(runCtx, "cmd", "/c", cmd.Command)
	} else {
		shell = exec.CommandContext(runCtx, "/bin/sh", "-c", cmd.Command)
	}
	shell.Dir = cwd
	shell.Env = ensureLocaleEnv(os.Environ())
	var output bytes.Buffer
	shell.Stdout = &output
	shell.Stderr = &output
	err := shell.Run()
	result := executorCommandResult{output: tailCommandOutput(output.String())}
	if runCtx.Err() == context.DeadlineExceeded {
		result.exitCode = 124
		result.errorMessage = fmt.Sprintf("command timed out after %ds", timeout)
		return result
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.exitCode = exitErr.ExitCode()
		} else {
			result.exitCode = 1
		}
		result.errorMessage = err.Error()
	}
	return result
}

func probeBuildHost(ctx context.Context, cfg config) executorCommandResult {
	var out bytes.Buffer
	home, _ := os.UserHomeDir()
	fmt.Fprintf(&out, "os=%s arch=%s cpus=%d hostname=%s user=%s home=%s\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), hostname(), os.Getenv("USER"), home)
	fmt.Fprintf(&out, "work_root=%s\n", commandWorkRoot(cfg))
	// 执行器自身的版本与安装时间：Agent 排查「改了代码但构建机还在跑旧二进制」时
	// 不用去 grep 二进制内容，直接看这一行。
	if exe, err := os.Executable(); err == nil {
		stamp := "stat=unknown"
		if st, statErr := os.Stat(exe); statErr == nil {
			stamp = fmt.Sprintf("mtime=%s size=%d", st.ModTime().UTC().Format(time.RFC3339), st.Size())
		}
		fmt.Fprintf(&out, "executor=%s path=%s %s\n", version, exe, stamp)
	}
	tools := []string{"node", "npm", "npx", "pnpm", "yarn", "go", "git", "xcodebuild", "xcrun", "codesign", "hdiutil", "brew", "docker", "makensis", "x86_64-w64-mingw32-g++"}
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			fmt.Fprintf(&out, "tool %s: missing\n", tool)
			continue
		}
		fmt.Fprintf(&out, "tool %s: %s\n", tool, path)
		version := toolVersion(ctx, tool)
		if version != "" {
			fmt.Fprintf(&out, "  version: %s\n", version)
		}
	}
	if runtime.GOOS != "windows" {
		// Agent 修环境前需要知道自己能不能提权：sudo 需要密码时只能选
		// 用户级安装，别在不可能成功的路径上反复尝试。
		sudoCtx, cancelSudo := context.WithTimeout(ctx, 5*time.Second)
		if err := exec.CommandContext(sudoCtx, "/bin/sh", "-c", "sudo -n true >/dev/null 2>&1").Run(); err == nil {
			fmt.Fprintf(&out, "sudo=passwordless\n")
		} else {
			fmt.Fprintf(&out, "sudo=requires-password\n")
		}
		cancelSudo()
		diskCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		disk := exec.CommandContext(diskCtx, "/bin/sh", "-c", "df -h . 2>&1; sysctl -n hw.memsize 2>/dev/null || true")
		disk.Dir = commandWorkRoot(cfg)
		if raw, err := disk.CombinedOutput(); err == nil {
			fmt.Fprintf(&out, "disk/memory:\n%s\n", strings.TrimSpace(string(raw)))
		}
		cancel()
	}
	if runtime.GOOS == "darwin" {
		// Node 24 的原生模块要用 V8 头；macOS 12 自带 libc++ 没有 ranges，
		// v8-memory-span.h 会编译失败。Agent 需要能一眼看到 SDK 与 clang 版本。
		ctxTools, cancelTools := context.WithTimeout(ctx, 8*time.Second)
		raw, err := exec.CommandContext(ctxTools, "/bin/sh", "-c",
			"xcode-select -p 2>/dev/null; xcrun --show-sdk-version 2>/dev/null; xcrun --show-sdk-path 2>/dev/null; clang --version 2>/dev/null | head -1").CombinedOutput()
		cancelTools()
		if err == nil {
			fmt.Fprintf(&out, "darwin toolchain:\n%s\n", strings.TrimSpace(string(raw)))
		}
		// 非交互 SSH 会话没有 LANG 时，NSIS Unicode 构建会以 std::bad_alloc 崩溃。
		localeCtx, cancelLocale := context.WithTimeout(ctx, 5*time.Second)
		localeRaw, _ := exec.CommandContext(localeCtx, "/bin/sh", "-c",
			`printf 'LANG=%s LC_ALL=%s LC_CTYPE=%s\n' "${LANG:-}" "${LC_ALL:-}" "${LC_CTYPE:-}"`).CombinedOutput()
		cancelLocale()
		fmt.Fprintf(&out, "locale: %s", strings.TrimSpace(string(localeRaw)))
		// Windows 安装包在 macOS 上交叉构建：NSIS 与 mingw-w64 缺一个就会
		// 在 assertInstallerTarget 处直接失败，probe 里先暴露出来。
		crossCtx, cancelCross := context.WithTimeout(ctx, 8*time.Second)
		cross, crossErr := exec.CommandContext(crossCtx, "/bin/sh", "-c",
			"makensis -VERSION 2>&1 | head -1; x86_64-w64-mingw32-g++ --version 2>/dev/null | head -1").CombinedOutput()
		cancelCross()
		if crossErr == nil {
			fmt.Fprintf(&out, "windows cross toolchain:\n%s\n", strings.TrimSpace(string(cross)))
		}
	}
	if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
		fmt.Fprintf(&out, "PATH=%s\n", path)
	}
	return executorCommandResult{output: tailCommandOutput(out.String())}
}

func toolVersion(ctx context.Context, tool string) string {
	args := []string{"--version"}
	switch tool {
	case "xcodebuild":
		args = []string{"-version"}
	case "xcode-select", "sw_vers":
		args = []string{"-version"}
	case "makensis":
		args = []string{"-VERSION"}
	case "codesign", "hdiutil":
		return ""
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(runCtx, tool, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(raw))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return line
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func commandLogs(cfg config, cmd *releasev2.ExecutorCommand) executorCommandResult {
	path, err := resolveCommandPath(cfg, stringPayload(cmd, "path"))
	if err != nil {
		return executorCommandResult{exitCode: 2, errorMessage: err.Error()}
	}
	lines := intPayload(cmd, "lines", 200, 2000)
	file, err := os.Open(path)
	if err != nil {
		return executorCommandResult{exitCode: 1, errorMessage: err.Error()}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return executorCommandResult{exitCode: 1, errorMessage: err.Error()}
	}
	limit := int64(2 << 20)
	if info.Size() > limit {
		if _, err := file.Seek(info.Size()-limit, io.SeekStart); err != nil {
			return executorCommandResult{exitCode: 1, errorMessage: err.Error()}
		}
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return executorCommandResult{exitCode: 1, errorMessage: err.Error()}
	}
	all := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if n := len(all); n > 0 && all[n-1] == "" {
		all = all[:n-1]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return executorCommandResult{output: strings.Join(all, "\n")}
}

func commandFiles(cfg config, cmd *releasev2.ExecutorCommand) executorCommandResult {
	path, err := resolveCommandPath(cfg, stringPayload(cmd, "path"))
	if err != nil {
		return executorCommandResult{exitCode: 2, errorMessage: err.Error()}
	}
	info, err := os.Stat(path)
	if err != nil {
		return executorCommandResult{exitCode: 1, errorMessage: err.Error()}
	}
	if !info.IsDir() {
		payload, _ := json.MarshalIndent(map[string]any{
			"path": path, "name": info.Name(), "size": info.Size(), "mode": info.Mode().String(),
			"modified": info.ModTime().UTC(), "dir": false,
		}, "", "  ")
		return executorCommandResult{output: string(payload)}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return executorCommandResult{exitCode: 1, errorMessage: err.Error()}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := make([]map[string]any, 0, len(entries))
	for i, entry := range entries {
		if i >= 500 {
			break
		}
		item := map[string]any{"name": entry.Name(), "dir": entry.IsDir()}
		if child, err := entry.Info(); err == nil {
			item["size"] = child.Size()
			item["mode"] = child.Mode().String()
			item["modified"] = child.ModTime().UTC()
		}
		items = append(items, item)
	}
	payload, _ := json.MarshalIndent(map[string]any{"path": path, "dir": true, "entries": items}, "", "  ")
	return executorCommandResult{output: string(payload)}
}

func tailCommandOutput(value string) string {
	// 命令输出可能夹带二进制（例如 grep 构建产物），NUL 会让控制面的
	// PostgreSQL 拒收整条结果，命令就永远停在 running。
	if strings.ContainsRune(value, 0) {
		value = strings.ReplaceAll(value, "\x00", "")
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "\uFFFD")
	}
	if len(value) <= maxCommandOutputBytes {
		return value
	}
	cut := len(value) - maxCommandOutputBytes
	for cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut++
	}
	return "...[truncated, showing tail]\n" + value[cut:]
}
