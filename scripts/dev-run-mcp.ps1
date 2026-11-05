# 后台启动 smartpipe-mcp 网关（本地开发用）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".dev\env.json"
if (-not (Test-Path -LiteralPath $envFile)) { throw "missing .dev/env.json" }
$cfg = Get-Content -LiteralPath $envFile -Raw | ConvertFrom-Json
foreach ($p in $cfg.PSObject.Properties) { Set-Item -Path ("env:" + $p.Name) -Value ([string]$p.Value) }
if (-not $env:SMARTPIPE_MCP_ADDR) { $env:SMARTPIPE_MCP_ADDR = ":8097" }

$exe = Join-Path $root "backend\bin\smartpipe-mcp.exe"
if (-not (Test-Path -LiteralPath $exe)) { throw "missing backend/bin/smartpipe-mcp.exe" }
$log = Join-Path $root ".dev\mcp.log"
$errLog = Join-Path $root ".dev\mcp.err.log"
$proc = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $errLog -PassThru
$proc.Id | Set-Content -LiteralPath (Join-Path $root ".dev\mcp.pid")
Start-Sleep -Seconds 2
if ($proc.HasExited) { Get-Content -LiteralPath $errLog -ErrorAction SilentlyContinue; exit 1 }
Write-Output ("mcp running pid=" + $proc.Id)