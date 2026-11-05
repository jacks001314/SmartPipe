# 后台启动 patch-verifier（本地开发用）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".dev\env.json"
if (-not (Test-Path -LiteralPath $envFile)) { throw "missing .dev/env.json" }
$cfg = Get-Content -LiteralPath $envFile -Raw | ConvertFrom-Json
foreach ($p in $cfg.PSObject.Properties) { Set-Item -Path ("env:" + $p.Name) -Value ([string]$p.Value) }

if (-not $env:SMARTPIPE_VERIFY_ADDR) { $env:SMARTPIPE_VERIFY_ADDR = ":8095" }
if (-not $env:SMARTPIPE_VERIFY_TOKEN) { $env:SMARTPIPE_VERIFY_TOKEN = "dev-verify-token" }
if (-not $env:SMARTPIPE_VERIFY_WORK_ROOT) { $env:SMARTPIPE_VERIFY_WORK_ROOT = Join-Path $root ".dev\verify-work" }

$exe = Join-Path $root "backend\bin\patch-verifier.exe"
if (-not (Test-Path -LiteralPath $exe)) { throw "missing backend/bin/patch-verifier.exe" }
$log = Join-Path $root ".dev\verifier.log"
$errLog = Join-Path $root ".dev\verifier.err.log"
$proc = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $errLog -PassThru
$proc.Id | Set-Content -LiteralPath (Join-Path $root ".dev\verifier.pid")
Start-Sleep -Seconds 2
if ($proc.HasExited) { Get-Content -LiteralPath $errLog -ErrorAction SilentlyContinue; exit 1 }
Write-Output ("patch-verifier running pid=" + $proc.Id)
