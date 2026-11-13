package adminapi

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/resourceprobe"
	"smartpipe/backend/internal/store"
)

// releaseV2ExecutorDeployRequest describes a build machine that should be
// provisioned over SSH: the console posts connection material, the server
// uploads the matching release-executor binary, writes its environment and
// starts it, so operators no longer copy a command onto every build machine.
type releaseV2ExecutorDeployRequest struct {
	ID           string `json:"id"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	AuthType     string `json:"auth_type"`
	Secret       string `json:"secret"`
	Passphrase   string `json:"passphrase"`
	ResourceID   string `json:"resource_id"`
	InstallDir   string `json:"install_dir"`
	SaveResource bool   `json:"save_resource"`
}

type releaseV2ExecutorDeployStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type releaseV2ExecutorDeployResult struct {
	OK          bool                          `json:"ok"`
	Error       string                        `json:"error,omitempty"`
	ExecutorID  string                        `json:"executor_id"`
	OS          string                        `json:"os"`
	Arch        string                        `json:"arch"`
	Host        string                        `json:"host"`
	Port        int                           `json:"port"`
	Username    string                        `json:"username"`
	InstallDir  string                        `json:"install_dir"`
	Fingerprint string                        `json:"fingerprint,omitempty"`
	ResourceID  string                        `json:"resource_id,omitempty"`
	Registered  bool                          `json:"registered"`
	Status      string                        `json:"status,omitempty"`
	LogTail     string                        `json:"log_tail,omitempty"`
	Steps       []releaseV2ExecutorDeployStep `json:"steps"`
}

var (
	executorIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	installDirPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

func (a *API) deployReleaseV2Executor(c iris.Context) {
	var in releaseV2ExecutorDeployRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	id := strings.TrimSpace(in.ID)
	if !executorIDPattern.MatchString(id) {
		a.badRequest(c, "构建机 id 只能包含字母、数字、点、下划线和短横线（最长 64 字符）")
		return
	}
	releaseToken := a.executorReleaseToken()
	if releaseToken == "" {
		a.badRequest(c, "服务端没有配置 release-service 令牌，无法部署构建机")
		return
	}
	wantOS := strings.ToLower(strings.TrimSpace(in.OS))
	if wantOS == "" {
		wantOS = "auto"
	}
	wantArch := strings.ToLower(strings.TrimSpace(in.Arch))
	if wantArch == "" {
		wantArch = "auto"
	}
	displayDir, dirExpr, err := executorInstallDir(in.InstallDir)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	result := releaseV2ExecutorDeployResult{
		ExecutorID: id, OS: wantOS, Arch: wantArch,
		InstallDir: displayDir, Steps: []releaseV2ExecutorDeployStep{},
	}
	step := func(name string, ok bool, detail string) {
		result.Steps = append(result.Steps, releaseV2ExecutorDeployStep{Name: name, OK: ok, Detail: detail})
	}
	fail := func(name, detail string) {
		step(name, false, detail)
		result.OK = false
		result.Error = detail
		_ = c.JSON(envelope{Data: result})
	}

	cfg, resourceID, err := a.executorSSHConfig(ctx, in)
	if err != nil {
		fail("读取 SSH 连接信息", err.Error())
		return
	}
	result.Host, result.Port, result.Username = cfg.Host, cfg.Port, cfg.Username
	result.ResourceID = resourceID

	client, observed, err := a.resourceProber.Connect(ctx, cfg)
	if err != nil {
		fail("SSH 连接", err.Error())
		return
	}
	defer client.Close()
	result.Fingerprint = observed
	step("SSH 连接", true, fmt.Sprintf("%s@%s:%d", cfg.Username, cfg.Host, portOf(cfg.Port)))

	if in.SaveResource && resourceID == "" {
		saved, saveErr := a.store.CreateSSHResource(ctx, store.SSHResourceInput{
			Name: "构建机 " + id, Description: "由发版 v2 SSH 部署创建",
			Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, AuthType: cfg.AuthType,
			Secret: cfg.Secret, Passphrase: cfg.Passphrase, HostKeyFingerprint: observed,
			Tags: []string{"build", "release"}, Enabled: true,
		})
		if saveErr != nil {
			step("保存 SSH 资源", false, saveErr.Error())
		} else {
			result.ResourceID = saved.ID
			step("保存 SSH 资源", true, "已按 build/release 标签保存到资源库")
		}
	}

	detectedOS, detectedArch, err := detectRemotePlatform(client)
	if err != nil {
		fail("识别构建机平台", err.Error())
		return
	}
	step("识别构建机平台", true, detectedOS+"/"+detectedArch)
	if wantOS != "auto" && wantOS != detectedOS {
		fail("校验平台", "选择的系统是 "+wantOS+"，构建机实际为 "+detectedOS)
		return
	}
	if wantArch != "auto" && wantArch != detectedArch {
		fail("校验平台", "选择的架构是 "+wantArch+"，构建机实际为 "+detectedArch)
		return
	}
	targetOS, targetArch := detectedOS, detectedArch
	result.OS, result.Arch = targetOS, targetArch
	if targetOS == "windows" {
		fail("校验平台", "Windows 构建机暂不支持 SSH 自动部署，请使用手动接入命令")
		return
	}

	binary, binErr := executorBinary(targetOS, targetArch)
	if binErr != nil {
		fail("准备执行器", binErr.Error())
		return
	}
	step("准备执行器", true, fmt.Sprintf("%s/%s %d MiB", targetOS, targetArch, len(binary)/(1024*1024)))

	setup := fmt.Sprintf(`set -e
DIR=%s
mkdir -p "$DIR"
`, dirExpr)
	if out, runErr := client.RunCmdTimeout(setup, 30*time.Second); runErr != nil {
		fail("创建安装目录", scrubDeployOutput(string(out), releaseToken, cfg.Secret))
		return
	}
	step("创建安装目录", true, displayDir)

	uploadCmd := fmt.Sprintf(`set -e
DIR=%s
cat > "$DIR/release-executor.new"
`, dirExpr)
	if out, runErr := client.RunCmdWithInput(uploadCmd, binary, 3*time.Minute); runErr != nil {
		fail("上传执行器", scrubDeployOutput(string(out), releaseToken, cfg.Secret))
		return
	}
	chmodCmd := fmt.Sprintf(`set -e
DIR=%s
chmod 0755 "$DIR/release-executor.new"
mv "$DIR/release-executor.new" "$DIR/release-executor"
`, dirExpr)
	if out, runErr := client.RunCmdTimeout(chmodCmd, 30*time.Second); runErr != nil {
		fail("安装执行器", scrubDeployOutput(string(out), releaseToken, cfg.Secret))
		return
	}
	step("上传执行器", true, "已写入 "+displayDir+"/release-executor")

	serviceURL := a.executorServiceURL(c)
	envFile := fmt.Sprintf(`SMARTPIPE_RELEASE_SERVICE_URL=%s
SMARTPIPE_RELEASE_SERVICE_TOKEN=%s
SMARTPIPE_RELEASE_EXECUTOR_ID=%s
SMARTPIPE_RELEASE_EXECUTOR_OS=%s
SMARTPIPE_RELEASE_EXECUTOR_ARCH=%s
`, shellQuoteValue(serviceURL), shellQuoteValue(releaseToken), shellQuoteValue(id), shellQuoteValue(targetOS), shellQuoteValue(targetArch))
	envCmd := fmt.Sprintf(`set -e
DIR=%s
cat > "$DIR/executor.env"
chmod 600 "$DIR/executor.env"
`, dirExpr)
	if out, runErr := client.RunCmdWithInput(envCmd, []byte(envFile), 30*time.Second); runErr != nil {
		fail("写入配置", scrubDeployOutput(string(out), releaseToken, cfg.Secret))
		return
	}
	step("写入配置", true, "服务地址 "+serviceURL)

	runScript := `#!/bin/sh
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
set -a
. "$DIR/executor.env"
set +a
exec "$DIR/release-executor"
`
	runCmd := fmt.Sprintf(`set -e
DIR=%s
cat > "$DIR/run.sh"
chmod 0755 "$DIR/run.sh"
`, dirExpr)
	if out, runErr := client.RunCmdWithInput(runCmd, []byte(runScript), 30*time.Second); runErr != nil {
		fail("写入启动脚本", scrubDeployOutput(string(out), releaseToken, cfg.Secret))
		return
	}
	step("写入启动脚本", true, displayDir+"/run.sh")

	startCmd := fmt.Sprintf(`set -e
DIR=%s
if [ -f "$DIR/executor.pid" ]; then
  OLD=$(cat "$DIR/executor.pid" 2>/dev/null || true)
  if [ -n "$OLD" ] && kill -0 "$OLD" 2>/dev/null; then kill "$OLD" 2>/dev/null || true; sleep 1; fi
fi
MODE=nohup
if [ "$(uname -s)" = "Darwin" ] && command -v launchctl >/dev/null 2>&1; then
  PLIST="$HOME/Library/LaunchAgents/com.smartpipe.release-executor.%s.plist"
  mkdir -p "$HOME/Library/LaunchAgents"
  printf '%%s\n' \
'<?xml version="1.0" encoding="UTF-8"?>' \
'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
'<plist version="1.0">' \
'<dict>' \
'  <key>Label</key><string>com.smartpipe.release-executor.%s</string>' \
'  <key>ProgramArguments</key>' \
'  <array><string>/bin/sh</string><string>'"$DIR"'/run.sh</string></array>' \
'  <key>RunAtLoad</key><true/>' \
'  <key>KeepAlive</key><true/>' \
'  <key>StandardOutPath</key><string>'"$DIR"'/executor.log</string>' \
'  <key>StandardErrorPath</key><string>'"$DIR"'/executor.log</string>' \
'</dict>' \
'</plist>' > "$PLIST"
  launchctl unload "$PLIST" >/dev/null 2>&1 || true
  if launchctl load -w "$PLIST" >/dev/null 2>&1; then MODE=launchd; fi
fi
if [ "$MODE" = nohup ] && [ "$(uname -s)" = "Linux" ] && command -v systemctl >/dev/null 2>&1; then
  UNITDIR="$HOME/.config/systemd/user"
  UNIT="$UNITDIR/smartpipe-release-executor-%s.service"
  mkdir -p "$UNITDIR"
  printf '%%s\n' \
'[Unit]' \
'Description=SmartPipe release executor %s' \
'[Service]' \
'ExecStart=/bin/sh '"$DIR"'/run.sh' \
'Restart=always' \
'RestartSec=3' \
'[Install]' \
'WantedBy=default.target' > "$UNIT"
  export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  systemctl --user daemon-reload >/dev/null 2>&1 || true
  if systemctl --user enable "smartpipe-release-executor-%s.service" >/dev/null 2>&1; then
    if systemctl --user restart "smartpipe-release-executor-%s.service" >/dev/null 2>&1; then MODE=systemd; fi
  fi
fi
if [ "$MODE" = nohup ]; then
  nohup sh "$DIR/run.sh" > "$DIR/executor.log" 2>&1 < /dev/null &
  echo $! > "$DIR/executor.pid"
fi
sleep 2
echo "MODE=$MODE"
if [ "$MODE" = nohup ]; then kill -0 "$(cat "$DIR/executor.pid")"; fi
`, dirExpr, id, id, id, id, id, id)
	startOut, runErr := client.RunCmdTimeout(startCmd, 90*time.Second)
	if runErr != nil {
		logTail, _ := client.RunCmdTimeout(fmt.Sprintf(`tail -n 20 %s/executor.log 2>/dev/null || true`, dirExpr), 15*time.Second)
		result.LogTail = scrubDeployOutput(string(logTail), releaseToken, cfg.Secret)
		fail("启动执行器", scrubDeployOutput(string(startOut), releaseToken, cfg.Secret))
		return
	}
	startMode := "nohup"
	if text := string(startOut); strings.Contains(text, "MODE=launchd") {
		startMode = "launchd"
	} else if strings.Contains(text, "MODE=systemd") {
		startMode = "systemd"
	}
	step("启动执行器", true, "启动方式 "+startMode)
	if err := a.store.UpsertExecutorHost(ctx, store.ExecutorHost{
		ExecutorID: id, ResourceID: result.ResourceID, Host: cfg.Host, Port: portOf(cfg.Port),
		Username: cfg.Username, InstallDir: displayDir,
	}); err != nil {
		step("记录部署位置", false, err.Error())
	} else {
		step("记录部署位置", true, displayDir)
	}

	registered := a.waitExecutorOnline(ctx, id, 45*time.Second)
	if registered {
		step("注册到 release-service", true, "执行器已在线")
		result.Registered = true
		result.Status = "online"
		result.OK = true
	} else {
		logTail, _ := client.RunCmdTimeout(fmt.Sprintf(`tail -n 25 %s/executor.log 2>/dev/null || true`, dirExpr), 15*time.Second)
		result.LogTail = scrubDeployOutput(string(logTail), releaseToken, cfg.Secret)
		step("注册到 release-service", false, "等待 45 秒仍未在线，请检查构建机到 "+serviceURL+" 的网络")
		result.OK = false
		result.Error = "执行器已启动但未完成注册"
	}

	outcome := "failed"
	if result.OK {
		outcome = "ok"
	}
	a.audit(ctx, c, "release.v2.executor.deploy", "release_executor", id, nil, outcome, map[string]any{
		"os": result.OS, "arch": result.Arch, "host": result.Host, "port": result.Port,
		"install_dir": displayDir, "registered": result.Registered,
	})
	_ = c.JSON(envelope{Data: result})
}

// executorSSHConfig resolves either an existing SSH resource or inline
// connection material. Plaintext secrets stay in memory.
func (a *API) executorSSHConfig(ctx context.Context, in releaseV2ExecutorDeployRequest) (resourceprobe.Config, string, error) {
	if rid := strings.TrimSpace(in.ResourceID); rid != "" {
		resource, secret, passphrase, err := a.store.GetSSHResourceSecret(ctx, rid)
		if err != nil {
			return resourceprobe.Config{}, "", fmt.Errorf("读取 SSH 资源失败：%w", err)
		}
		return resourceprobe.Config{
			Host: resource.Host, Port: resource.Port, Username: resource.Username,
			AuthType: resource.AuthType, Secret: secret, Passphrase: passphrase,
			PinnedFingerprint: resource.HostKeyFingerprint,
		}, rid, nil
	}
	host := strings.TrimSpace(in.Host)
	username := strings.TrimSpace(in.Username)
	secret := in.Secret
	if host == "" || username == "" || strings.TrimSpace(secret) == "" {
		return resourceprobe.Config{}, "", fmt.Errorf("请填写构建机地址、用户名和密码/私钥，或选择一个已保存的 SSH 资源")
	}
	authType := strings.ToLower(strings.TrimSpace(in.AuthType))
	if authType == "" {
		authType = "password"
	}
	if authType != "password" && authType != "private_key" {
		return resourceprobe.Config{}, "", fmt.Errorf("认证方式只支持 password / private_key")
	}
	return resourceprobe.Config{
		Host: host, Port: in.Port, Username: username, AuthType: authType,
		Secret: secret, Passphrase: in.Passphrase,
	}, "", nil
}

func executorInstallDir(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "$HOME/.smartpipe/release-executor"
	}
	if rest, ok := strings.CutPrefix(raw, "$HOME/"); ok {
		cleaned := path.Clean(rest)
		if rest == "" || cleaned == "." || strings.HasPrefix(cleaned, "..") || !installDirPattern.MatchString(rest) {
			return "", "", fmt.Errorf("安装目录包含不支持的字符")
		}
		return raw, `"$HOME/` + cleaned + `"`, nil
	}
	if !strings.HasPrefix(raw, "/") || !installDirPattern.MatchString(raw) {
		return "", "", fmt.Errorf("安装目录必须是绝对路径，或留空使用 $HOME/.smartpipe/release-executor")
	}
	cleaned := path.Clean(raw)
	if cleaned == "/" || len(strings.Split(strings.Trim(cleaned, "/"), "/")) < 2 {
		return "", "", fmt.Errorf("安装目录层级过浅，请使用 /opt/... 或 $HOME/... 下的子目录")
	}
	return raw, "'" + cleaned + "'", nil
}

func detectRemotePlatform(client *resourceprobe.SSHClient) (string, string, error) {
	out, err := client.RunCmdTimeout("uname -s 2>/dev/null; uname -m 2>/dev/null", 20*time.Second)
	if err != nil {
		return "", "", fmt.Errorf("远端系统识别失败：%v", err)
	}
	lines := strings.Fields(strings.ReplaceAll(string(out), "\r", "\n"))
	if len(lines) < 2 {
		return "", "", fmt.Errorf("远端没有返回 uname 信息（当前只支持 Linux / macOS）")
	}
	osName := strings.ToLower(lines[0])
	switch {
	case strings.Contains(osName, "darwin"):
		osName = "darwin"
	case strings.Contains(osName, "linux"):
		osName = "linux"
	case strings.Contains(osName, "mingw"), strings.Contains(osName, "msys"), strings.Contains(osName, "cygwin"):
		osName = "windows"
	default:
		return "", "", fmt.Errorf("暂不支持的系统：%s", lines[0])
	}
	arch := strings.ToLower(lines[1])
	switch arch {
	case "x86_64", "amd64":
		arch = "amd64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("暂不支持的架构：%s", lines[1])
	}
	return osName, arch, nil
}

func executorBinary(osName, arch string) ([]byte, error) {
	dir := strings.TrimSpace(os.Getenv("SMARTPIPE_EXECUTOR_ASSETS"))
	if dir == "" {
		dir = "/app/bin/executors"
	}
	name := "release-executor"
	if osName == "windows" {
		name = "release-executor.exe"
	}
	candidates := []string{
		filepath.Join(dir, osName+"-"+arch, name),
		filepath.Join(dir, name),
	}
	// A developer build may only have the native binary in /app/bin.
	if osName == runtime.GOOS && arch == runtime.GOARCH {
		candidates = append(candidates, filepath.Join("/app/bin", name))
	}
	for _, candidate := range candidates {
		if data, err := os.ReadFile(candidate); err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return nil, fmt.Errorf("服务端缺少 %s/%s 的执行器包（%s），请重新构建镜像", osName, arch, filepath.Join(dir, osName+"-"+arch, name))
}

func (a *API) waitExecutorOnline(ctx context.Context, id string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ex, err := a.store.GetExecutor(ctx, id); err == nil {
			fresh := ex.LastHeartbeatAt != nil && time.Since(*ex.LastHeartbeatAt) < 90*time.Second
			if strings.EqualFold(ex.Status, "online") && fresh {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}

func portOf(port int) int {
	if port == 0 {
		return 22
	}
	return port
}

func shellQuoteValue(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func scrubDeployOutput(text, token, secret string) string {
	out := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if token != "" {
		out = strings.ReplaceAll(out, token, "***")
	}
	if secret != "" && len(secret) > 3 {
		out = strings.ReplaceAll(out, secret, "***")
	}
	if len(out) > 1200 {
		out = out[:1200] + "…"
	}
	return out
}
