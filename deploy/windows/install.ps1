#requires -Version 5.1
<#
.SYNOPSIS
在 Windows 构建机上安装 release-executor，并注册为开机自启的计划任务。

.DESCRIPTION
执行器是 pull 模式：这台 Windows 只向 release-service 发请求，不开入站端口、不需要 SSH。
脚本做四件事：拷二进制、写受 ACL 保护的 executor.env、安装 run-executor.ps1 包装器、
注册并启动计划任务 SmartPipeReleaseExecutor（SYSTEM 账户，开机触发）。

.EXAMPLE
.\install.ps1 -BinaryPath .\release-executor.exe -ServiceUrl http://10.0.0.5:18099 -ExecutorId win-01 -Token <token>

.EXAMPLE
.\install.ps1 -BinaryPath .\release-executor.exe -ServiceUrl http://10.0.0.5:18099 -ExecutorId win-01 -DryRun
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)][string]$BinaryPath,
    [string]$ServiceUrl = $env:SMARTPIPE_RELEASE_SERVICE_URL,
    [string]$ExecutorId = $env:SMARTPIPE_RELEASE_EXECUTOR_ID,
    [string]$Token = $env:SMARTPIPE_RELEASE_SERVICE_TOKEN,
    [string]$InstallDir = 'C:\ProgramData\SmartPipe\release-executor',
    [string]$EnvFile = '',
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$TaskName = 'SmartPipeReleaseExecutor'
$BinDir = Join-Path $InstallDir 'bin'
$LogDir = Join-Path $InstallDir 'logs'
$ConfigPath = Join-Path $InstallDir 'executor.env'
$RunnerPath = Join-Path $InstallDir 'run-executor.ps1'
$ExePath = Join-Path $BinDir 'release-executor.exe'

function Write-Step([string]$Message) {
    if ($DryRun) { Write-Host "[dry-run] $Message" } else { Write-Host $Message }
}

if (-not (Test-Path -LiteralPath $BinaryPath)) { throw "找不到二进制：$BinaryPath" }
$BinaryPath = (Resolve-Path -LiteralPath $BinaryPath).Path
if (-not $DryRun) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw '需要管理员权限：请用“以管理员身份运行”的 PowerShell 执行。'
    }
}
if ([string]::IsNullOrWhiteSpace($ServiceUrl)) {
    if ($DryRun) { $ServiceUrl = '<prompt>' } else { $ServiceUrl = (Read-Host 'release-service URL（例如 http://10.0.0.5:18099）') }
}
if ([string]::IsNullOrWhiteSpace($ExecutorId)) { $ExecutorId = ($env:COMPUTERNAME).ToLowerInvariant() }
if ([string]::IsNullOrWhiteSpace($Token)) {
    if ($DryRun) {
        $Token = '<prompt>'
    } else {
        $secure = Read-Host 'executor token' -AsSecureString
        $Token = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
    }
}

$envLines = @(
    "SMARTPIPE_RELEASE_SERVICE_URL=$ServiceUrl",
    "SMARTPIPE_RELEASE_SERVICE_TOKEN=$Token",
    "SMARTPIPE_RELEASE_EXECUTOR_ID=$ExecutorId"
)
if ($EnvFile) {
    if (-not (Test-Path -LiteralPath $EnvFile)) { throw "找不到环境变量文件：$EnvFile" }
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
        if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
        $envLines += $line.TrimEnd()
    }
}

Write-Step "安装目录：$InstallDir"
Write-Step "拷贝 $BinaryPath -> $ExePath"
Write-Step "写入 $ConfigPath（只允许 SYSTEM/Administrators 读取）"
Write-Step "安装包装器 $RunnerPath"
Write-Step "注册计划任务 $TaskName（SYSTEM，开机启动，失败每分钟重启）"

if ($DryRun) {
    Write-Host ''
    Write-Host 'executor.env 将包含：'
    $envLines | ForEach-Object {
        if ($_ -match '^SMARTPIPE_RELEASE_SERVICE_TOKEN=') { 'SMARTPIPE_RELEASE_SERVICE_TOKEN=***' } else { $_ }
    }
    Write-Host ''
    Write-Host 'dry-run 结束，未修改系统。'
    return
}

New-Item -ItemType Directory -Force -Path $BinDir, $LogDir | Out-Null
Copy-Item -LiteralPath $BinaryPath -Destination $ExePath -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'run-executor.ps1') -Destination $RunnerPath -Force
[IO.File]::WriteAllLines($ConfigPath, $envLines, (New-Object System.Text.UTF8Encoding($false)))

$acl = Get-Acl -LiteralPath $ConfigPath
$acl.SetAccessRuleProtection($true, $false)
foreach ($rule in @($acl.Access)) { $acl.RemoveAccessRule($rule) | Out-Null }
foreach ($who in @('NT AUTHORITY\SYSTEM', 'BUILTIN\Administrators')) {
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($who, 'FullControl', 'Allow'))) | Out-Null
}
Set-Acl -LiteralPath $ConfigPath -AclObject $acl

$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$RunnerPath`""
$trigger = New-ScheduledTaskTrigger -AtStartup
$taskPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $taskPrincipal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName

Write-Host ''
Write-Host "已安装并启动计划任务 $TaskName。"
Write-Host "状态：Get-ScheduledTask -TaskName $TaskName | Get-ScheduledTaskInfo"
Write-Host "日志：$LogDir\release-executor.log"
Write-Host "卸载：.\uninstall.ps1"