# 后台启动 bridge 服务（本地开发用）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".dev\env.json"
if (-not (Test-Path -LiteralPath $envFile)) { throw "missing .dev/env.json" }
$cfg = Get-Content -LiteralPath $envFile -Raw | ConvertFrom-Json
foreach ($p in $cfg.PSObject.Properties) { Set-Item -Path ("env:" + $p.Name) -Value ([string]$p.Value) }

if (-not $env:SMARTPIPE_BRIDGE_ADDR) { $env:SMARTPIPE_BRIDGE_ADDR = ":8090" }
if (-not $env:SMARTPIPE_BRIDGE_TOKEN) { $env:SMARTPIPE_BRIDGE_TOKEN = "dev-bridge-token" }
if (-not $env:GITHUB_WEBHOOK_SECRET) { $env:GITHUB_WEBHOOK_SECRET = "local-webhook-secret-123" }

$exe = Join-Path $root "backend\bin\bridge.exe"
if (-not (Test-Path -LiteralPath $exe)) { throw "missing backend/bin/bridge.exe" }
$log = Join-Path $root ".dev\bridge.log"
$errLog = Join-Path $root ".dev\bridge.err.log"
$proc = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $errLog -PassThru
$proc.Id | Set-Content -LiteralPath (Join-Path $root ".dev\bridge.pid")
Start-Sleep -Seconds 2
if ($proc.HasExited) { Get-Content -LiteralPath $errLog -ErrorAction SilentlyContinue; exit 1 }
Write-Output ("bridge running pid=" + $proc.Id)
