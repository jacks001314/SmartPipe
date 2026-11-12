// Package resourceprobe verifies SSH resources and collects a compact host
// profile. Commands are static and read-only; credentials never enter command
// text, logs, or returned errors.
package resourceprobe

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"smartpipe/backend/internal/store"
)

// Config is the decrypted connection material for one SSH resource.
type Config struct {
	Host              string
	Port              int
	Username          string
	AuthType          string
	Secret            string
	Passphrase        string
	PinnedFingerprint string
}

// Result describes a connection test.
type Result struct {
	OK            bool
	Message       string
	LatencyMS     int64
	Fingerprint   string
	ServerVersion string
}

// Inspection is a successful or failed host profile collection.
type Inspection struct {
	Result
	SystemInfo map[string]any
}

// Prober owns SSH timeouts. It is cheap and safe to reuse across requests; each
// call creates and closes its own transport.
type Prober struct {
	ConnectTimeout time.Duration
	CommandTimeout time.Duration
}

func New() *Prober {
	return &Prober{ConnectTimeout: 15 * time.Second, CommandTimeout: 25 * time.Second}
}

// Connect returns an authenticated SSH client for another backend module such
// as release or verification. The caller owns the returned client and must
// close it. The observed fingerprint is returned even when authentication or
// execution later fails.
func (p *Prober) Connect(ctx context.Context, cfg Config) (*SSHClient, string, error) {
	return p.connect(ctx, cfg)
}

// RunCommand is the shared execution primitive for modules that build or test
// on a selected SSH resource. The caller decides the command; credentials stay
// in the SSH transport and never become part of command text.
func (p *Prober) RunCommand(ctx context.Context, cfg Config, command string, timeout time.Duration) ([]byte, string, error) {
	client, observed, err := p.connect(ctx, cfg)
	if err != nil {
		return nil, observed, err
	}
	defer client.Close()
	if timeout <= 0 {
		timeout = p.commandTimeout()
	}
	output, err := client.RunCmdTimeout(command, timeout)
	return output, observed, err
}

// Test establishes SSH authentication and executes a harmless marker command.
func (p *Prober) Test(ctx context.Context, cfg Config) Result {
	start := time.Now()
	client, observed, err := p.connect(ctx, cfg)
	if err != nil {
		return Result{Message: safeConnectionError(err, cfg, observed), LatencyMS: time.Since(start).Milliseconds(), Fingerprint: observed}
	}
	defer client.Close()
	output, err := client.RunCmdTimeout(`printf "smartpipe-ssh-ready\n"`, p.commandTimeout())
	if err != nil {
		return Result{Message: "SSH 已认证，但远端命令执行失败：" + scrub(err.Error(), cfg), LatencyMS: time.Since(start).Milliseconds(), Fingerprint: observed, ServerVersion: serverVersion(client)}
	}
	if !strings.Contains(string(output), "smartpipe-ssh-ready") {
		return Result{Message: "SSH 登录成功，但远端 shell 未返回预期标记", LatencyMS: time.Since(start).Milliseconds(), Fingerprint: observed, ServerVersion: serverVersion(client)}
	}
	return Result{
		OK: true, Message: "SSH 登录成功，远端 shell 可执行命令",
		LatencyMS: time.Since(start).Milliseconds(), Fingerprint: observed,
		ServerVersion: serverVersion(client),
	}
}

// Inspect authenticates and gathers OS, CPU, memory, disk, GPU and toolchain
// information with a fixed, read-only shell script.
func (p *Prober) Inspect(ctx context.Context, cfg Config) Inspection {
	start := time.Now()
	client, observed, err := p.connect(ctx, cfg)
	if err != nil {
		return Inspection{Result: Result{Message: safeConnectionError(err, cfg, observed), LatencyMS: time.Since(start).Milliseconds(), Fingerprint: observed}}
	}
	defer client.Close()
	output, err := client.RunCmdTimeout(systemInfoScript, p.commandTimeout())
	result := Result{LatencyMS: time.Since(start).Milliseconds(), Fingerprint: observed, ServerVersion: serverVersion(client)}
	if err != nil {
		result.Message = "SSH 已认证，但系统信息采集失败：" + scrub(err.Error(), cfg)
		return Inspection{Result: result}
	}
	info := ParseSystemInfo(string(output))
	if len(info) == 0 {
		result.Message = "SSH 已认证，但远端没有返回可识别的系统信息"
		return Inspection{Result: result, SystemInfo: map[string]any{}}
	}
	result.OK = true
	result.Message = "连接正常，系统信息已采集"
	return Inspection{Result: result, SystemInfo: info}
}

func (p *Prober) connect(ctx context.Context, cfg Config) (*SSHClient, string, error) {
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.Secret) == "" {
		return nil, "", errors.New("SSH 地址、用户名和密码/私钥不能为空")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	expected := strings.TrimSpace(cfg.PinnedFingerprint)
	observed := ""
	hostKeyCallback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		observed = ssh.FingerprintSHA256(key)
		if expected != "" && subtle.ConstantTimeCompare([]byte(observed), []byte(expected)) != 1 {
			return errors.New("SSH host key fingerprint does not match the pinned fingerprint")
		}
		return nil
	}
	client, err := SecureLogin(ctx, SecureLoginOptions{
		Addr: addr, User: cfg.Username, Auth: cfg.Secret,
		IsPasswd:   cfg.AuthType == store.ResourceAuthPassword,
		Passphrase: cfg.Passphrase, Timeout: p.connectTimeout(),
		HostKeyCallback: hostKeyCallback, KeyboardAuth: true,
	})
	if err != nil {
		return nil, observed, err
	}
	return client, observed, nil
}

func (p *Prober) connectTimeout() time.Duration {
	if p != nil && p.ConnectTimeout > 0 {
		return p.ConnectTimeout
	}
	return 15 * time.Second
}

func (p *Prober) commandTimeout() time.Duration {
	if p != nil && p.CommandTimeout > 0 {
		return p.CommandTimeout
	}
	return 25 * time.Second
}

func serverVersion(client *SSHClient) string {
	if client == nil || client.RawClient() == nil {
		return ""
	}
	return strings.TrimSpace(string(client.RawClient().ServerVersion()))
}

func safeConnectionError(err error, cfg Config, observed string) string {
	if observed != "" && strings.TrimSpace(cfg.PinnedFingerprint) != "" && observed != strings.TrimSpace(cfg.PinnedFingerprint) {
		return "主机指纹不匹配：服务器返回 " + observed + "。请核对目标主机，确认后更新固定指纹"
	}
	msg := scrub(err.Error(), cfg)
	switch {
	case strings.Contains(strings.ToLower(msg), "unable to authenticate"):
		return "SSH 认证失败，请检查用户名、密码/私钥或口令"
	case strings.Contains(strings.ToLower(msg), "connection refused"):
		return "SSH 端口不可达：目标主机拒绝连接，请检查地址、端口和 SSH 服务"
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(strings.ToLower(msg), "timeout"):
		return "SSH 连接超时，请检查网络、代理和防火墙"
	case strings.Contains(strings.ToLower(msg), "no such host"):
		return "无法解析目标主机名"
	}
	if observed != "" {
		return "SSH 登录失败（主机指纹 " + observed + "）：" + truncate(msg, 240)
	}
	return "SSH 登录失败：" + truncate(msg, 240)
}

func scrub(value string, cfg Config) string {
	value = strings.TrimSpace(value)
	if cfg.Secret != "" {
		value = strings.ReplaceAll(value, cfg.Secret, "****")
	}
	if cfg.Passphrase != "" {
		value = strings.ReplaceAll(value, cfg.Passphrase, "****")
	}
	return value
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// ParseSystemInfo turns the probe's tab-separated output into a stable JSON
// object. Unknown fields are ignored to avoid reflecting arbitrary host data.
func ParseSystemInfo(output string) map[string]any {
	info := map[string]any{"captured_at": time.Now().UTC().Format(time.RFC3339)}
	tools := map[string]string{}
	gpus := make([]map[string]any, 0, 2)
	var memoryKB, diskTotalKB, diskAvailableKB uint64
	for _, line := range strings.Split(output, "\n") {
		if len(line) > 2048 {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "\t")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if value == "" || strings.ContainsRune(value, '\x00') {
			continue
		}
		switch key {
		case "hostname", "os", "kernel", "architecture", "cpu_model", "login_user", "home":
			info[key] = truncate(value, 300)
		case "cpu_count":
			if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 1_000_000 {
				info[key] = n
			}
		case "memory_total_kb":
			memoryKB, _ = strconv.ParseUint(value, 10, 64)
		case "disk_total_kb":
			diskTotalKB, _ = strconv.ParseUint(value, 10, 64)
		case "disk_available_kb":
			diskAvailableKB, _ = strconv.ParseUint(value, 10, 64)
		case "uptime_seconds":
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				info[key] = n
			}
		case "tool_docker", "tool_git", "tool_go", "tool_node", "tool_npm", "tool_python":
			tools[strings.TrimPrefix(key, "tool_")] = truncate(value, 180)
		case "gpu":
			if len(gpus) >= 32 {
				continue
			}
			parts := strings.Split(value, ",")
			gpu := map[string]any{"name": truncate(strings.TrimSpace(parts[0]), 180)}
			if len(parts) > 1 {
				gpu["memory_mb"] = strings.TrimSpace(parts[1])
			}
			if len(parts) > 2 {
				gpu["driver_version"] = strings.TrimSpace(parts[2])
			}
			gpus = append(gpus, gpu)
		}
	}
	if memoryKB > 0 {
		info["memory_total_bytes"] = memoryKB * 1024
	}
	if diskTotalKB > 0 {
		info["disk_total_bytes"] = diskTotalKB * 1024
		info["disk_available_bytes"] = diskAvailableKB * 1024
	}
	if len(tools) > 0 {
		info["tools"] = tools
	}
	if len(gpus) > 0 {
		info["gpus"] = gpus
	}
	if len(info) == 1 {
		return map[string]any{}
	}
	return info
}

const systemInfoScript = `export LC_ALL=C
printf "hostname\t%s\n" "$(hostname 2>/dev/null || uname -n 2>/dev/null)"
printf "os\t%s\n" "$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release 2>/dev/null | head -n 1 | tr -d '"')"
printf "kernel\t%s\n" "$(uname -r 2>/dev/null)"
printf "architecture\t%s\n" "$(uname -m 2>/dev/null)"
printf "cpu_model\t%s\n" "$(awk -F: '/model name/ {gsub(/^ +/,"",$2); print $2; exit}' /proc/cpuinfo 2>/dev/null)"
if [ -z "$(awk -F: '/model name/ {gsub(/^ +/,"",$2); print $2; exit}' /proc/cpuinfo 2>/dev/null)" ]; then
  printf "cpu_model\t%s\n" "$(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
fi
printf "cpu_count\t%s\n" "$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null)"
printf "memory_total_kb\t%s\n" "$(awk '/MemTotal/ {print $2; exit}' /proc/meminfo 2>/dev/null)"
printf "disk_total_kb\t%s\n" "$(df -Pk / 2>/dev/null | awk 'NR==2 {print $2; exit}')"
printf "disk_available_kb\t%s\n" "$(df -Pk / 2>/dev/null | awk 'NR==2 {print $4; exit}')"
printf "uptime_seconds\t%s\n" "$(awk '{print int($1); exit}' /proc/uptime 2>/dev/null)"
printf "login_user\t%s\n" "$(id -un 2>/dev/null)"
printf "home\t%s\n" "$HOME"
printf "tool_docker\t%s\n" "$(docker version --format "{{.Server.Version}}" 2>/dev/null)"
printf "tool_git\t%s\n" "$(git --version 2>/dev/null)"
printf "tool_go\t%s\n" "$(go version 2>/dev/null)"
printf "tool_node\t%s\n" "$(node --version 2>/dev/null)"
printf "tool_npm\t%s\n" "$(npm --version 2>/dev/null)"
printf "tool_python\t%s\n" "$(python3 --version 2>/dev/null)"
nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits 2>/dev/null | head -n 32 | while IFS= read -r line; do printf "gpu\t%s\n" "$line"; done
`
