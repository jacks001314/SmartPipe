$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$env:LANXIN_APP_ID = "fake-app"
$env:LANXIN_APP_SECRET = "fake-secret"
$env:LANXIN_API_BASE = "http://127.0.0.1:9092"
$env:BOT_HTTP_ADDR = ":8093"
$env:INTERNAL_TOKEN = "dev-internal-token"
$env:CRASH_INGEST_URL = "http://127.0.0.1:8090/ingest/crash"
$env:CRASH_INGEST_TOKEN = "dev-crash-token"
$env:CRASH_PROJECT_ID = "prj_5148b505"
$exe = Join-Path $root "backend\bin\bot.exe"
if (-not (Test-Path -LiteralPath $exe)) { throw "missing backend/bin/bot.exe" }
$log = Join-Path $root ".dev\bot.log"
$errLog = Join-Path $root ".dev\bot.err.log"
$proc = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $errLog -PassThru
$proc.Id | Set-Content -LiteralPath (Join-Path $root ".dev\bot.pid")
Start-Sleep -Seconds 2
if ($proc.HasExited) { Get-Content -LiteralPath $errLog -ErrorAction SilentlyContinue; exit 1 }
Write-Output ("bot running pid=" + $proc.Id)
