# Full local SIT regression: health checks, backend tests, web build, and every smoke script.
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\sit-all.ps1 [-SkipTests] [-Only mcp-smoke]
param(
  [switch]$SkipTests,
  [switch]$SkipWeb,
  [string]$Only = ""
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$results = New-Object System.Collections.ArrayList

function Add-Result([string]$name, [bool]$ok, [string]$detail) {
  $null = $results.Add([pscustomobject]@{ Step = $name; OK = $ok; Detail = $detail })
  $flag = if ($ok) { "PASS" } else { "FAIL" }
  Write-Output ("[{0}] {1} {2}" -f $flag, $name, $detail)
}

function Check-Health([string]$name, [string]$url) {
  try {
    $resp = Invoke-WebRequest -Uri $url -TimeoutSec 5 -UseBasicParsing
    Add-Result ("health:" + $name) ($resp.StatusCode -eq 200) ("HTTP " + $resp.StatusCode)
    return $resp.StatusCode -eq 200
  } catch {
    Add-Result ("health:" + $name) $false $_.Exception.Message
    return $false
  }
}

function Run-Step([string]$name, [scriptblock]$body) {
  $sw = [Diagnostics.Stopwatch]::StartNew()
  try {
    $out = & $body 2>&1
    $sw.Stop()
    $text = ($out | Select-Object -Last 2) -join " "
    Add-Result $name $true ((("{0:N1}s " -f $sw.Elapsed.TotalSeconds)) + $text)
  } catch {
    $sw.Stop()
    Add-Result $name $false ((("{0:N1}s " -f $sw.Elapsed.TotalSeconds)) + $_.Exception.Message)
  }
}

$services = @(
  @{ n = "admin-api"; u = "http://127.0.0.1:8092/healthz" },
  @{ n = "bridge"; u = "http://127.0.0.1:8090/healthz" },
  @{ n = "patch-verifier"; u = "http://127.0.0.1:8095/healthz" },
  @{ n = "mcp"; u = "http://127.0.0.1:8097/healthz" },
  @{ n = "fake-github"; u = "http://127.0.0.1:9099/__state" },
  @{ n = "fake-bot"; u = "http://127.0.0.1:9091/__replies" },
  @{ n = "fake-lanxin"; u = "http://127.0.0.1:9092/__messages" },
  @{ n = "bot"; u = "http://127.0.0.1:8093/healthz" }
)
Write-Output "== services =="
foreach ($s in $services) { $null = Check-Health $s.n $s.u }

if (-not $SkipTests) {
  Write-Output "== backend =="
  Run-Step "go-vet" {
    Push-Location (Join-Path $root "backend")
    try { $env:GOPROXY = "https://goproxy.cn,direct"; & go vet ./...; if ($LASTEXITCODE -ne 0) { throw "go vet failed" } } finally { Pop-Location }
  }
  Run-Step "go-test" {
    Push-Location (Join-Path $root "backend")
    try {
      $env:GOPROXY = "https://goproxy.cn,direct"
      if (-not $env:SMARTPIPE_TEST_DSN) {
        $cfg = Get-Content -LiteralPath (Join-Path $root ".dev\env.json") -Raw | ConvertFrom-Json
        $env:SMARTPIPE_TEST_DSN = ([string]$cfg.SMARTPIPE_DSN).Replace("/smartpipe?", "/smartpipe_test?")
      }
      & go test -p 1 ./... | Out-Null
      if ($LASTEXITCODE -ne 0) { throw "go test failed" }
      "all packages ok"
    } finally { Pop-Location }
  }
}

if (-not $SkipWeb) {
  Write-Output "== web =="
  Run-Step "web-build" {
    Push-Location (Join-Path $root "web")
    try { & npm run build | Out-Null; if ($LASTEXITCODE -ne 0) { throw "npm build failed" } ; "tsc + vite ok" } finally { Pop-Location }
  }
}

$smokes = @("mcp-smoke", "repo-merge-smoke", "approval-smoke", "bridge-smoke", "crash-smoke", "bot-crash-smoke", "release-smoke", "e2e-issue-smoke")
if ($Only) { $smokes = @($Only) }
Write-Output "== smokes =="
foreach ($name in $smokes) {
  $path = Join-Path $root ("scripts\" + $name + ".ps1")
  if (-not (Test-Path -LiteralPath $path)) { Add-Result $name $false "script missing"; continue }
  Run-Step $name {
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $path 2>&1
    $code = $LASTEXITCODE
    $last = $out | Where-Object { $_ -match "OK|FAIL|ERR" } | Select-Object -Last 1
    if ($code -ne 0) { throw ("exit=" + $code + " " + $last) }
    "exit=0 " + $last
  }
}

Write-Output ""
Write-Output "== summary =="
$failed = @($results | Where-Object { -not $_.OK })
foreach ($r in $results) {
  $flag = "PASS"
  if (-not $r.OK) { $flag = "FAIL" }
  Write-Output ("{0,-28} {1,-5} {2}" -f $r.Step, $flag, $r.Detail)
}
if ($failed.Count -gt 0) {
  Write-Output ("SIT FAILED: " + $failed.Count + "/" + $results.Count + " steps failed")
  exit 1
}
Write-Output ("SIT ALL OK: " + $results.Count + " steps")
