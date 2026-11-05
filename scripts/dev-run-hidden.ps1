# 以隐藏窗口后台启动 admin-api（本地开发用）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".dev\env.json"
if (-not (Test-Path -LiteralPath $envFile)) { throw "missing .dev/env.json" }
$cfg = Get-Content -LiteralPath $envFile -Raw | ConvertFrom-Json
foreach ($p in $cfg.PSObject.Properties) { Set-Item -Path ("env:" + $p.Name) -Value ([string]$p.Value) }
$env:SMARTPIPE_WEB_DIST = Join-Path $root "web\dist"

$exe = Join-Path $root "backend\bin\admin-api.exe"
if (-not (Test-Path -LiteralPath $exe)) { throw "missing backend/bin/admin-api.exe" }

$log = Join-Path $root ".dev\admin.log"
$errLog = Join-Path $root ".dev\admin.err.log"
$proc = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $errLog -PassThru
$proc.Id | Set-Content -LiteralPath (Join-Path $root ".dev\admin.pid")
Start-Sleep -Seconds 3
if ($proc.HasExited) {
    Write-Output "admin-api exited; stderr:"
    Get-Content -LiteralPath $errLog -ErrorAction SilentlyContinue
    exit 1
}
Write-Output ("running pid=" + $proc.Id)
