package adminapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/resourceprobe"
	"smartpipe/backend/internal/store"
)

type executorRemoveRequest struct {
	ResourceID string `json:"resource_id"`
	InstallDir string `json:"install_dir"`
	Force      bool   `json:"force"`
}

type executorRemoveResult struct {
	OK            bool                          `json:"ok"`
	Error         string                        `json:"error,omitempty"`
	ExecutorID    string                        `json:"executor_id"`
	Host          string                        `json:"host,omitempty"`
	InstallDir    string                        `json:"install_dir,omitempty"`
	RemoteCleaned bool                          `json:"remote_cleaned"`
	Steps         []releaseV2ExecutorDeployStep `json:"steps"`
}

// removeReleaseV2Executor 删除一台构建执行器：先回目标机停止服务、删掉安装目录，
// 再清理注册记录。没有部署记录时只删注册信息，需要显式 force。
func (a *API) removeReleaseV2Executor(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	if !executorIDPattern.MatchString(id) {
		a.badRequest(c, "构建机 id 不合法")
		return
	}
	var in executorRemoveRequest
	_ = decodeBody(c, &in)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result := executorRemoveResult{ExecutorID: id, Steps: []releaseV2ExecutorDeployStep{}}
	step := func(name string, ok bool, detail string) {
		result.Steps = append(result.Steps, releaseV2ExecutorDeployStep{Name: name, OK: ok, Detail: detail})
	}

	active, err := a.store.CountActiveTargetsForExecutor(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	if active > 0 && !in.Force {
		c.StatusCode(409)
		_ = c.JSON(envelope{Err: &apiError{
			Code:    "executor_busy",
			Message: fmt.Sprintf("还有 %d 个 target 正在使用该构建机，确认要删除请选择强制删除", active),
		}})
		return
	}

	host, herr := a.store.GetExecutorHost(ctx, id)
	if herr != nil && !errors.Is(herr, store.ErrNotFound) {
		a.fail(c, herr)
		return
	}
	if host == nil {
		if strings.TrimSpace(in.ResourceID) == "" && strings.TrimSpace(in.InstallDir) == "" {
			step("查找部署记录", false, "没有部署记录，只能删除注册信息")
		} else {
			host = &store.ExecutorHost{ExecutorID: id, ResourceID: strings.TrimSpace(in.ResourceID), InstallDir: strings.TrimSpace(in.InstallDir)}
		}
	} else {
		result.Host, result.InstallDir = host.Host, host.InstallDir
	}

	remoteCleaned := false
	if host != nil && strings.TrimSpace(host.ResourceID) != "" {
		displayDir, dirExpr, derr := executorInstallDir(host.InstallDir)
		if derr != nil {
			step("校验安装目录", false, derr.Error())
		} else {
			resource, secret, passphrase, serr := a.store.GetSSHResourceSecret(ctx, host.ResourceID)
			if serr != nil {
				step("读取 SSH 资源", false, "读取失败："+serr.Error())
			} else {
				if result.Host == "" {
					result.Host = resource.Host
				}
				result.InstallDir = displayDir
				cfg := resourceprobe.Config{
					Host: resource.Host, Port: resource.Port, Username: resource.Username,
					AuthType: resource.AuthType, Secret: secret, Passphrase: passphrase,
					PinnedFingerprint: resource.HostKeyFingerprint,
				}
				client, _, cerr := a.resourceProber.Connect(ctx, cfg)
				if cerr != nil {
					step("SSH 连接", false, cerr.Error())
				} else {
					out, rerr := client.RunCmdTimeout(executorUninstallScript(dirExpr, id), 90*time.Second)
					client.Close()
					if rerr != nil {
						step("停止并卸载", false, scrubDeployOutput(string(out), "", secret))
					} else {
						remoteCleaned = true
						step("停止并卸载", true, "已停止服务并删除 "+displayDir)
					}
				}
			}
		}
	}

	if !remoteCleaned && !in.Force {
		result.OK = false
		result.Error = "远程卸载未完成，注册记录已保留；确认只删除注册信息可选择强制删除"
		_ = c.JSON(envelope{Data: result})
		return
	}

	if err := a.store.DeleteExecutor(ctx, id); err != nil {
		a.fail(c, err)
		return
	}
	_ = a.store.DeleteExecutorHost(ctx, id)
	step("删除注册记录", true, "已从执行器列表移除")
	if host != nil && strings.TrimSpace(host.ResourceID) != "" {
		if resource, rerr := a.store.GetSSHResource(ctx, host.ResourceID); rerr == nil && strings.TrimSpace(resource.Name) == "构建机 "+id {
			if derr := a.store.DeleteSSHResource(ctx, resource.ID); derr == nil {
				step("删除 SSH 资源", true, resource.Name)
			} else {
				step("删除 SSH 资源", false, derr.Error())
			}
		}
	}

	result.OK = remoteCleaned
	result.RemoteCleaned = remoteCleaned
	if !remoteCleaned {
		result.Error = "远程卸载未完成，已按强制删除只清理注册记录"
	}
	outcome := "ok"
	if !remoteCleaned {
		outcome = "partial"
	}
	a.audit(ctx, c, "release.v2.executor.delete", "release_executor", id, nil, outcome, map[string]any{
		"host": result.Host, "install_dir": result.InstallDir, "remote_cleaned": remoteCleaned,
	})
	_ = c.JSON(envelope{Data: result})
}

// executorUninstallScript 在目标机上停掉 launchd/systemd/nohup 三种启动方式，
// 删掉服务文件与安装目录。脚本对“目录不像执行器安装”的情况拒绝删除。
func executorUninstallScript(dirExpr, id string) string {
	return fmt.Sprintf(`DIR=%s
case "$DIR" in
  ""|"/"|"$HOME"|"$HOME/") echo "refuse to remove $DIR" >&2; exit 3 ;;
esac
if [ -d "$DIR" ] && [ ! -f "$DIR/run.sh" ] && [ ! -f "$DIR/release-executor" ] && [ ! -f "$DIR/release-executor.exe" ]; then
  echo "refuse: $DIR does not look like an executor install" >&2
  exit 3
fi
if [ -f "$DIR/executor.pid" ]; then
  OLD=$(cat "$DIR/executor.pid" 2>/dev/null || true)
  if [ -n "$OLD" ] && kill -0 "$OLD" 2>/dev/null; then kill "$OLD" 2>/dev/null || true; sleep 1; fi
fi
PLIST="$HOME/Library/LaunchAgents/com.smartpipe.release-executor.%s.plist"
if [ -f "$PLIST" ]; then
  launchctl unload -w "$PLIST" >/dev/null 2>&1 || true
  rm -f "$PLIST" || true
fi
UNIT="$HOME/.config/systemd/user/smartpipe-release-executor-%s.service"
if [ -f "$UNIT" ]; then
  systemctl --user disable --now "$(basename "$UNIT")" >/dev/null 2>&1 || true
  rm -f "$UNIT" || true
  systemctl --user daemon-reload >/dev/null 2>&1 || true
fi
pkill -f "^$DIR/release-executor" >/dev/null 2>&1 || true
rm -rf "$DIR"
echo "UNINSTALLED $DIR"
`, dirExpr, id, id)
}
