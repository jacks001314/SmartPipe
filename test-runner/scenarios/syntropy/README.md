# Syntropy 真机热替换验收场景

本目录把「已安装的 Syntropy」变成可被 SmartPipe 在线测试系统调度的候选环境：
替换组件 → 启动 → 独立检查 → 截图留证 → 清理还原。判定与证据仍由 SmartPipe
后端完成，Runner 只是执行器。

## 场景 `syntropy-desktop-hotswap@1`

由 `BundleSwapScenario` 实现，一次运行做这些事：

1. 校验候选包 `candidate.tar.gz` 的 SHA-256 等于任务里的 `artifact_sha256`。
2. 解包并逐个校验 `bundle.manifest.json` 中组件的 SHA-256。
3. 备份安装目录下将被替换的文件到 `<stateRoot>/backups/<runId>/`。
4. 执行 `stop`（停掉 Syntropy 及其子进程）。
5. 用候选文件覆盖目标路径（目标必须是安装根目录内的相对路径）。
6. 执行 `start`，恢复并等待主窗口出现。
7. 依次执行 `checks`，可把每个检查产出的文件作为该检查的证据上传。
8. 清理时先停进程，再按备份还原，并核对还原后的 SHA-256，最后执行 `cleanup` 校验。

任何一步失败都不会自动重试；机器保持占用，需管理员在控制台确认清理后恢复。

例外是“尚未改动安装目录”的预检失败：候选包哈希不符、解包/清单错误、组件哈希
不符、目标路径越界、场景或平台不匹配。这些情况 Runner 会提交一份全部检查为
`BLOCKED` 的报告，随后正常清理并释放机器，不需要人工恢复。只有已经停止进程或
覆盖过文件之后的失败才保持占用，等待管理员核对现场。

## 可热替换范围（实测）

已验证可以热替换并正常启动的组件：

- `resources/node/artifacts/*`（`nbot`、`mbot`、`node-runtime` 等独立二进制）
- `resources/release-manifest.json`（修改组件时必须同步更新对应 `size` / `sha256`）
- `resources/plugins/*`、`resources/data/*` 等安装目录内的普通文件

**不能只替换 `resources/app.asar`**。当前安装版本启用了 Electron asar 完整性校验，
替换后会直接退出并写入：

```text
FATAL:electron\shell\common\asar\asar_util.cc:143] Integrity check failed for asar archive
```

桌面 UI 代码的变更需要走完整安装包/升级包，或在专门的测试构建里关闭 asar 完整性
Fuse；不要把 `app.asar` 放进本场景的候选包。

### 引擎热替换计划与恢复

引擎路径使用显式 `hotswap/plan.json`，步骤支持 `copy`/`remove`/`stop_process`/
`start_process`/`wait_http`/`exec`/`sleep`/`note`。两点运维约定：

- `stop_process` 支持 `tree: true`，并会等待进程真正退出后再继续（Windows 上镜像
  句柄未释放时覆盖文件会失败）；
- 计划应提供 `restore_steps`，在任务结束、备份还原后把产品重新拉起；否则被停掉的
  应用会保持停止状态。

真机验证（2026-11-19）：`session-c24a3ff2d69cf76845db20d2cae9df90` 在
`%LOCALAPPDATA%\Programs\Syntropy` 上替换 `resources/plugins/quant/ui/styles.css`，
重启 Syntropy 并通过 CDP 截图确认主界面；任务结束后 `styles.css` 还原为原始
sha256 `02665f68…`，Syntropy 由 `restore_steps` 重新拉起，`9334/json/version`
返回 200。

## 制作候选包

```powershell
node build-bundle.mjs --spec hotswap.json --out C:\SmartPipeTest\artifacts\candidate.tar.gz --root .
```

`hotswap.json`（完整示例见 `hotswap.example.json`）：

```json
{
  "version": "0.5.21.2026-11-17",
  "components": [
    { "slot": "nbot", "path": "dist/nbot-windows-amd64.exe",
      "target": "resources/node/artifacts/nbot-windows-amd64.exe", "mode": 493 },
    { "slot": "release-manifest", "path": "dist/release-manifest.json",
      "target": "resources/release-manifest.json" }
  ]
}
```

`target` 相对安装根目录；`path` 相对 `--root`，也可写绝对路径；`mode` 仅对 Unix
生效。打包器输出的 `artifact_sha256` 就是创建测试任务时要填的制品摘要。
`version` 会以 `SMARTPIPE_EXPECT_VERSION` 注入检查命令。

## Windows 测试机准备

在测试机上执行（先把本仓库的 `test-runner` 目录拷过去）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\deploy-runner.ps1 `
  -InstallRoot "$env:LOCALAPPDATA\Programs\Syntropy" `
  -ApiURL http://127.0.0.1:8092 `
  -MachineID machine-xxxx -Token <机器令牌> `
  -TunnelHost 192.168.197.109
```

脚本会：复制 Runner 与场景脚本、把令牌写入仅当前用户可读的
`C:\SmartPipeTest\machine.token`、生成 `worker.json`、注册计划任务
`SmartPipeTestRunner`（`LogonType Interactive`）。

指定 `-TunnelHost` 时，`worker.json` 会带上隧道配置；`run-worker.ps1` 在每次领取
前建立 `127.0.0.1:<LocalPort> -> <TunnelHost>:127.0.0.1:<RemotePort>` 的 SSH
隧道，任务结束后关闭。这样 Windows 端可以用回环地址 `http://127.0.0.1:8092`
访问 SmartPipe，无需为局域网 HTTP 放行或额外配置 TLS。相关参数：
`-TunnelUser`、`-TunnelLocalPort`、`-TunnelRemotePort`、`-SshKey`、`-SshExe`。

启动一次领取：

```powershell
schtasks /run /tn SmartPipeTestRunner
```

让测试机常驻轮询（推荐的长期用法）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\deploy-runner.ps1 `
  -InstallRoot "$env:LOCALAPPDATA\Programs\Syntropy" `
  -ApiURL http://127.0.0.1:8092 -MachineID machine-xxxx -Token <机器令牌> `
  -TunnelHost 192.168.197.109 -Loop -IntervalSec 20 -AtLogOn
schtasks /run /tn SmartPipeTestRunner
```

`-Loop` 让 `run-worker.ps1` 常驻，空闲时按 `-IntervalSec` 间隔重复领取；
`-AtLogOn` 额外注册登录触发，测试用户登录桌面后自动恢复轮询。计划任务使用
`IgnoreNew` 多实例策略，脚本内再用 `Local\SmartPipeTestRunner` 互斥量兜底，
不会同时跑两个领取进程。

领取使用 `<stateRoot>\desktop.lock`。进程被强杀或机器重启后留下的锁会在
有效期结束、记录中的 PID 已退出、或锁文件损坏且超过 10 秒时被下一个 Runner
安全接管；仍在运行的有效锁不会被抢占。

必须用交互式会话运行：Windows 的 GUI 只能在已登录桌面上有效，SSH/session 0
里启动的进程看不到桌面。计划任务会运行在已登录用户的会话中。

## 截图与锁屏

`syntropy-ctl.ps1 -Action screenshot` 会先恢复最小化到托盘的窗口，再用
`PrintWindow` 抓取窗口自身的像素；如果 `PrintWindow` 失败或结果为空白，才回退到
`CopyFromScreen`。注意：Electron/Chromium 在**断开的 RDP 会话**里不再绘制，
`PrintWindow` 会成功但只返回纯灰面。这类场景请给 Syntropy 加调试端口，并让引擎
通过 CDP 截图：

- 启动参数：`Syntropy.exe --remote-debugging-port=0 --remote-allow-origins=*`
  （固定端口也可以；Electron 42 不写 `DevToolsActivePort`，所以 0 表示随机端口）
- `session.json`：`"screenshotCDPPort": 9334`（首选）、`"screenshotCDPURLMatch": "app://"`、
  `"screenshotCDPProcess": "Syntropy"`；首选端口失败时引擎会扫描该进程的监听端口并探测
  `/json/version`，命中后再 CDP 截图。引擎顺序为 CDP → 标题窗口 → 全屏，事件里会记录 `via cdp`。
- `worker.json` 可配置 `desktopApp`，让常驻 worker 在登录后自动拉起应用：

```json
"desktopApp": {
  "executable": "C:\\Users\\windows\\AppData\\Local\\Programs\\Syntropy\\Syntropy.exe",
  "processName": "Syntropy",
  "args": ["--remote-debugging-port=9334", "--remote-allow-origins=*"],
  "waitSec": 20
}
```

`deploy-runner.ps1` 现在支持 `-DesktopDebugPort 9334`、`-ScreenshotTitle Syntropy`、
`-UseEngine`、`-EnginePort 8790`、`-CodexCommand <codex.exe>`，会把这些字段写进
`session.json` 并生成 `desktopApp`。

Windows 上被强杀的进程可能残留僵尸监听端口；如果新实例的 CDP 端口连不上，
换一个端口或重启测试机。

`-Action wait` 会在窗口出现后等待 `-SettleSec` 秒（默认 25 秒），让“正在连接
工作区”的闪屏过渡到主界面。这只是稳定性等待，不能代替业务断言。

## 检查项

`deploy-runner.ps1` 默认生成：

| 检查项 | 含义 |
|---|---|
| `app_swapped` | 按 bundle 清单逐个核对安装目录中的目标文件 SHA-256 |
| `app_running` | `Syntropy.exe` 及其子进程存在 |
| `app_window` | 主窗口出现、恢复且完成稳定等待 |
| `app_version` | 安装目录 `release-manifest.json` 的 `productVersion` 等于候选包版本 |
| `nbot_smoke` | 执行替换后的 `nbot dns`，要求退出码为 0 |
| `app_screenshot` | 截取主窗口为 `syntropy-main.png` 并作为证据上传 |

任务创建时的 `required_checks` 必须与本地 checks 的 id 完全一致；后端只按
`scenario@version` 分配任务，下发的检查项不能超出本地已部署集合。

## 限制

- 只证明候选文件被替换、进程能启动、窗口出现；业务正确性仍要靠检查脚本独立断言。
- `desktop-observe` 不能作为发布门禁；本场景可配置为门禁策略，但需先确认检查项真的覆盖了
  产品行为。
- 进程树按「可执行文件位于安装根目录」递归终止；由 Syntropy 拉起的、路径在安装根之外的
  子进程需要额外清理脚本。
- `app_version` 读取的是安装目录的 `release-manifest.json`。只换 `app.asar`（如果
  测试构建关闭了完整性校验）时，必须把新的 `release-manifest.json` 一并放进候选包。
- Linux 目前只提供组件级（mbot/nbot）热替换验证，桌面截图需要 GNOME 会话内的辅助脚本。

## 会话型测试（Agent 故事）

发布门禁之外，同一台机器可以执行由 Agent 驱动的故事。Runner 常驻进程先领取发布
运行，再领取会话；两者共用 `desktop.lock` 和 `Local\SmartPipeTestRunner` 互斥量，
不会同时操作桌面。

部署：

```powershell
.\windows\deploy-runner.ps1 -InstallRoot 'C:\Users\windows\AppData\Local\Programs\Syntropy' `
  -ApiURL 'http://127.0.0.1:8092' -MachineID 'machine-...' -Token '<token>' `
  -InstallSessionHost -Loop -IntervalSec 5 -AtLogOn
```

`deploy-runner.ps1` 会生成：

- `session.json`：允许根目录、可执行白名单、截图脚本、隧道配置。
- `worker.json` 的 `sessionConfig`：让 `run-worker.ps1` 改用 `dispatch-cli.js`。

会话创建后，Agent 通过 `dist/src/test-mcp.js` 的 MCP 工具执行故事。操作员可先用
`examples/session-smoke.mjs` 在不接入模型的情况下验证整条链路：

```powershell
$env:SMARTPIPE_API_URL='http://127.0.0.1:18092'
$env:SMARTPIPE_TEST_SESSION_ID='session-...'
$env:SMARTPIPE_TEST_SESSION_TOKEN='...'
node test-runner\examples\session-smoke.mjs
```

故事放在 `stories/`，是纯 Markdown；`required_checks` 仍在服务端判定，Agent 自报的
PASS 不能跳过必测项和证据校验。`stories/install-smoke.md` 只验证安装和主窗口，
`stories/local-backend-health.md` 验证 local master/gateway；两者不能互相替代。
