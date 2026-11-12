#requires -Version 5.1
<#
.SYNOPSIS
停止并移除 Windows 构建机上的 release-executor 计划任务。
#>
[CmdletBinding()]
param(
    [string]$InstallDir = 'C:\ProgramData\SmartPipe\release-executor',
    [switch]$RemoveFiles
)
$ErrorActionPreference = 'Stop'
$TaskName = 'SmartPipeReleaseExecutor'

$task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($task) {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Host "已移除计划任务 $TaskName"
} else {
    Write-Host "计划任务 $TaskName 不存在，跳过"
}
Get-Process -Name 'release-executor' -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -like "$InstallDir*" } |
    ForEach-Object { Stop-Process -Id $_.Id -Force }

if ($RemoveFiles) {
    if (Test-Path -LiteralPath $InstallDir) {
        Remove-Item -LiteralPath $InstallDir -Recurse -Force
        Write-Host "已删除 $InstallDir"
    }
} else {
    Write-Host "保留 $InstallDir（含日志与 executor.env）；要删除加 -RemoveFiles"
}