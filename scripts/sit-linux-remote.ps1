# 在 Linux 主机上跑 sit-linux.sh：打包 backend+sdks → 上传 → 解包 → 执行。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\sit-linux-remote.ps1 `
#         -HostName 192.168.1.99 -User jacks -PassFile C:\path\pass.txt [-SkipTest]
param(
  [string]$HostName = "192.168.1.99",
  [string]$User = "jacks",
  [string]$PassFile = "",
  [string]$TestDSN = "postgres://smartpipe:smartpipe_dev_2026@192.168.1.99:5432/smartpipe_test?sslmode=disable",
  [switch]$SkipTest
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$tar = Join-Path $root (".dev\sit-linux-" + $stamp + ".tar.gz")

Push-Location $root
try {
  if (Test-Path -LiteralPath $tar) { Remove-Item -LiteralPath $tar -Force }
  & tar.exe -czf $tar --exclude=backend/bin --exclude=backend/*.exe --exclude=backend/*.test backend sdks scripts/sit-linux.sh scripts/linux-runtime-smoke.sh
  if ($LASTEXITCODE -ne 0) { throw "tar failed" }
  Write-Output ("[pack] " + $tar + " " + (Get-Item -LiteralPath $tar).Length + " bytes")
} finally { Pop-Location }

$remoteCmd = "SMARTPIPE_TEST_DSN='" + $TestDSN + "' bash scripts/sit-linux.sh"
if ($SkipTest) { $remoteCmd = "bash scripts/sit-linux.sh --skip-test" }

$pyArgs = @("scripts\remote-run.py", "--host", $HostName, "--user", $User, "--local", $tar, "--dir", "/tmp/smartpipe-sit", "--cmd", $remoteCmd)
if ($PassFile) { $pyArgs += @("--pass-file", $PassFile) }
Push-Location $root
try { & python @pyArgs } finally { Pop-Location }
$code = $LASTEXITCODE
Remove-Item -LiteralPath $tar -Force -ErrorAction SilentlyContinue
if ($code -ne 0) { Write-Output ("LINUX SIT FAILED (exit " + $code + ")"); exit $code }
Write-Output "LINUX SIT PASSED (remote)"