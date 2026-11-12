# Windows 构建机接入（无需 SSH）

构建机是 **pull 模式**：这台 Windows 主动向 release-service 拉任务，不开入站端口、不需要 SSH 登录，也不需要把机器账号交给平台。执行器的 `runShell` 在 Windows 上用 `cmd /C` 执行 build/sign/verify 命令。

## 1. 准备二进制

在任意机器上交叉编译：

```powershell
cd backend
$env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -ldflags "-s -w -X main.version=0.6.9" -o dist/release-executor.exe ./cmd/release-executor
```

把 `release-executor.exe` 与 `deploy/windows` 下的三个脚本拷到这台 Windows。

## 2. 安装并常驻

以管理员身份打开 PowerShell：

```powershell
.\install.ps1 -BinaryPath .\release-executor.exe `
    -ServiceUrl http://10.0.0.5:18099 `
    -ExecutorId win-01 `
    -Token <executor token>
```

脚本会：

- 把二进制装到 `C:\ProgramData\SmartPipe\release-executor\bin`；
- 写 `executor.env`（服务地址、执行器令牌），ACL 只保留 `SYSTEM` 与 `Administrators`；
- 安装 `run-executor.ps1`：加载 env、把 `git` 与 Windows Kits 加入 PATH、日志落盘并按 10MB 轮转；
- 注册计划任务 `SmartPipeReleaseExecutor`：`SYSTEM` 账户、开机触发、失败每分钟重启、无执行时长上限。

先看会做什么可以加 `-DryRun`，不会修改系统。额外的环境变量（私有仓库凭据、签名口令等）用 `-EnvFile` 传入一个 `KEY=VALUE` 文本：

```powershell
.\install.ps1 -BinaryPath .\release-executor.exe -ServiceUrl ... -ExecutorId win-01 -Token ... `
    -EnvFile .\win-secrets.env
```

### 2.1 改脚本时的编码要求

`install.ps1`、`run-executor.ps1`、`uninstall.ps1` 都含中文注释。Windows PowerShell 5.1 在没有 BOM 时按系统 ANSI 代码页读脚本，UTF-8 中文会被读成乱码并连带语法解析失败，典型报错是 `Unexpected token '}'`。因此这三个文件必须保存为 **UTF-8 with BOM**。用 PowerShell 7 或编辑器改完，按下面方式补回 BOM：

```powershell
$path = '.\install.ps1'
$text = Get-Content -LiteralPath $path -Raw
[IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($true)))
```

改完先用解析器自查，不执行脚本：

```powershell
foreach ($f in Get-ChildItem *.ps1) {
    $errs = $null
    $null = [System.Management.Automation.Language.Parser]::ParseFile($f.FullName, [ref]$null, [ref]$errs)
    if ($errs) { "$($f.Name): FAIL" } else { "$($f.Name): OK" }
}
```

新增的 `.ps1` 只写 ASCII 时可以不带 BOM；出现任何非 ASCII 字符就按上面的方式存。

## 3. 在控制台确认

- 执行器列表出现 `win-01`（`windows/amd64`）。
- 计划目标 `windows-amd64` 会自动绑定到它，或用 `executor_bindings` 指定。
- 日志：`C:\ProgramData\SmartPipe\release-executor\logs\release-executor.log`。

## 4. 私有仓库与凭据

执行器用 `git.exe` 克隆，先装 Git for Windows（`C:\Program Files\Git\cmd` 已由包装器加入 PATH）。私有仓库在 `smartpipe.yaml` 里写引用名：

```yaml
release:
  source:
    credential: git-test
```

构建机的 `executor.env` 里放 `SMARTPIPE_CRED_GIT_TEST=<token>`；需要指定用户名时加 `SMARTPIPE_CRED_GIT_TEST_USER=oauth2`（GitHub 默认 `x-access-token`，其它主机默认 `git`）。令牌只存在这台机器，不进入计划、运行记录与日志。

## 5. 签名

目标上的 `sign:` 在 build 之后、verify 与上传之前执行，走 `cmd /C`，可以用 `%VAR%` 引用机器环境变量：

```yaml
release:
  targets:
    windows-amd64:
      build: "powershell -NoProfile -Command \"...\""
      sign: "signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 /f C:\\ProgramData\\SmartPipe\\release-executor\\certs\\sign.pfx /p %SMARTPIPE_WIN_CERT_PASSWORD% dist\\app.exe"
      verify: "powershell -NoProfile -Command \"if (-not (Test-Path dist\\app.exe)) { exit 1 }\""
```

证书放在 `C:\ProgramData\SmartPipe\release-executor\certs`，口令放 `executor.env`。签名失败会让目标以 `sign: ...` 失败，不会上传半成品；`policies.signing.required: true` 时未签名制品会被 `update_channel_promote` / `release_publish` 拒绝。

## 6. 升级与卸载

- 升级：重跑 `install.ps1`（`Register-ScheduledTask -Force` 覆盖旧任务）。
- 卸载：`.\uninstall.ps1`（加 `-RemoveFiles` 连目录一起删）。
- 计划任务以 `SYSTEM` 运行，桌面上的 `Get-Process` 看不到它；查状态用 `Get-ScheduledTask` 与日志文件。

## 7. 已验证与未验证

已验证：

- `GOOS=windows GOARCH=amd64` 交叉编译通过，二进制可启动；
- `install.ps1 -DryRun` 输出安装步骤，令牌在输出中脱敏；
- `run-executor.ps1` 在 PowerShell 5.1 下加载 env、启动执行器、正常退出并把日志写进 `logs\release-executor.log`；
- 真机端到端（Windows + Git for Windows，经 SSH 隧道连测试机的 release-service）：注册为 `windows/amd64`、领取 `windows-amd64` 目标、用 `source.credential` 克隆私有仓库、`cmd /C` 构建、执行 `sign:` 钩子、verify、预签名上传。运行 `rrun_8e872eecc74f0415` 到达 `built`，制品 `app-bundle` 为 `sha256:65b05c63…`、`platform_signed=true`。

联调中发现并修掉的 Windows 特有坑（已进执行器代码与脚本）：

1. 全局代理：构建机常配 `HTTP(S)_PROXY`，git 会把 `127.0.0.1` 也交给代理；注入凭据时把回环地址并入 `NO_PROXY`/`no_proxy`。
2. Git Credential Manager 抢 askpass：注入凭据时用 `-c credential.helper=` 禁用系统凭据助手，避免弹登录窗卡住克隆。
3. askpass 可执行文件：Windows 不能直接把 `.sh`/`.cmd` 当 `GIT_ASKPASS` 执行；改为让 `release-executor` 自身进入 askpass 模式（`SMARTPIPE_ASKPASS_MODE=1`，令牌仍只在环境变量里）。
4. `cmd /C` 引号：Go `os/exec` 对含内嵌引号的命令参数转义与 cmd 语义不一致，`powershell -Command "..."` 会被当成字符串打印；改为写临时 `.cmd` 文件再执行，并有回归测试。
5. 预签名地址：上传地址取服务端配置的 S3 endpoint，配成 `127.0.0.1:9000` 时远端构建机不可达；生产必须配构建机可达的内网地址（联调时用端口隧道临时打通）。
6. 脚本编码：无 BOM 的 UTF-8 脚本在 PowerShell 5.1 下解析失败（见 2.1），三个 `.ps1` 已统一存成 UTF-8 with BOM。
7. 日志编码：早先用 `*>>` 重定向时日志变成 UTF-16，看着全是 NUL 字节；改为 `& $exe *>&1 | Out-File -Append -Encoding utf8`，日志现在是带 BOM 的 UTF-8，可直接 `Get-Content` 或 `tail`。

未验证：

- `signtool` 真实签名与时间戳（需要代码签名证书）；
- Windows Server 上计划任务的长期运行与重启恢复；
- 计划任务在无桌面会话时的网络出口策略（企业代理需在 `executor.env` 里配 `HTTPS_PROXY`/`NO_PROXY`）。