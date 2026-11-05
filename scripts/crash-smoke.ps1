# 崩溃上报联调：真实 bridge + Postgres + 假 GitHub。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\crash-smoke.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$bridge = "http://127.0.0.1:8090"
$api = "http://127.0.0.1:8092/admin"
$gh = "http://127.0.0.1:9099"
$token = "dev-crash-token"
$project = "prj_5148b505"
$user = "smoke-user-" + [guid]::NewGuid().ToString("N").Substring(0, 8)

function PostJson($url, $payload, $headers) {
  $tmp = Join-Path $env:TEMP ("smartpipe-crash-" + [guid]::NewGuid().ToString("N") + ".json")
  [IO.File]::WriteAllText($tmp, ($payload | ConvertTo-Json -Depth 12 -Compress), [Text.UTF8Encoding]::new($false))
  $argv = @("-s", "-o", "$tmp.out", "-w", "%{http_code}", "-X", "POST",
    "-H", "Content-Type: application/json")
  foreach ($k in $headers.Keys) { $argv += @("-H", ($k + ": " + $headers[$k])) }
  $argv += @("--data-binary", "@$tmp", $url)
  $status = & curl.exe @argv
  $body = (Get-Content -LiteralPath "$tmp.out" -Raw) | ConvertFrom-Json
  return @{ Status = [int]$status; Body = $body }
}

function Bundle($stack) {
  return @{
    app = "desktop-client"; version = "4.2.1"; build = "20261101.3"
    os = "Windows 11 23H2"; arch = "amd64"; user_id = $user; device_id = "dev-1"
    occurred_at = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    exception = @{
      type = "NullReferenceException"
      message = "Object reference not set; token ghp_ABCDEFGHIJKLMNOPQRSTUVWX leaked"
      stack = $stack
    }
    logs = @("open login form", "contact alice@example.com", "path C:\Users\alice\AppData\Local")
    metadata = @{ module = "login" }
  }
}

$h = @{ "X-Ingest-Token" = $token }
$tag = [guid]::NewGuid().ToString("N").Substring(0, 6)
$stackA = "at Login.Form.Show_$tag() in C:\work\Login.cs:line 42`nat Login.Main_$tag() in C:\work\Main.cs:line 7"
$stackB = "at Export.Worker.Run_$tag() in C:\work\Export.cs:line 88"

$r1 = PostJson ($bridge + "/ingest/crash") @{ project_id = $project; source = "lanxin"; bundle = (Bundle $stackA) } $h
if ($r1.Status -ne 201 -or $r1.Body.data.status -ne "created") { throw ("first ingest failed: " + $r1.Status + " " + ($r1.Body | ConvertTo-Json -Depth 6)) }
$issue1 = $r1.Body.data.issue_number
$fp1 = $r1.Body.data.report.fingerprint
if (-not $issue1 -or -not $fp1) { throw "missing issue or fingerprint" }

$r2 = PostJson ($bridge + "/ingest/crash") @{ project_id = $project; bundle = (Bundle $stackA) } $h
if ($r2.Status -ne 200 -or $r2.Body.data.status -ne "deduped") { throw ("dedupe failed: " + $r2.Status + " " + ($r2.Body | ConvertTo-Json -Depth 6)) }
if ($r2.Body.data.occurrences -ne 2 -or $r2.Body.data.issue_number -ne $issue1) { throw "dedupe should bump the same issue" }

$r3 = PostJson ($bridge + "/ingest/crash") @{ project_id = $project; bundle = (Bundle $stackB) } $h
if ($r3.Status -ne 201 -or $r3.Body.data.status -ne "created") { throw ("second fingerprint failed: " + $r3.Status) }

$r4 = PostJson ($bridge + "/ingest/crash") @{ project_id = $project; bundle = (Bundle "at Other.Frame_$tag() in C:\work\Other.cs:line 1") } $h
if ($r4.Status -ne 429 -or $r4.Body.data.status -ne "rate_limited") { throw ("rate limit failed: " + $r4.Status) }

$big = Bundle $stackA
$big.logs = @("x" * 300000)
$r5 = PostJson ($bridge + "/ingest/crash") @{ project_id = $project; bundle = $big } $h
if ($r5.Status -ne 422 -or $r5.Body.data.status -ne "rejected") { throw ("oversize failed: " + $r5.Status) }

$issue = Invoke-RestMethod -Uri ($gh + "/repos/example/release/issues/" + $issue1)
$labelNames = ($issue.labels | ForEach-Object { $_.name }) -join ","
if ($labelNames -notmatch "crash_report") { throw ("labels = " + $labelNames) }
if ($issue.body -match "ghp_" -or $issue.body -match "alice@example.com" -or $issue.body -match "C:\\Users\\alice") { throw "issue body not redacted" }
if ($issue.body -notmatch "\[REDACTED\]") { throw "expected redaction marker in issue body" }
if ($issue.body -notmatch "crash_report #" + $r1.Body.data.report.id) { throw "issue body should reference the report id" }

$adminHeaders = @{ Authorization = "Bearer dev-admin-token" }
$list = Invoke-RestMethod -Uri ($api + "/crashes?limit=20") -Headers $adminHeaders
$found = $list.data | Where-Object { $_.id -eq $r1.Body.data.report.id }
if (-not $found) { throw "admin list missing crash report" }
$detail = Invoke-RestMethod -Uri ($api + "/crashes/" + $found.id) -Headers $adminHeaders
if (($detail.data.bundle | ConvertTo-Json -Depth 8) -match "ghp_") { throw "stored bundle still has a token" }

Write-Output ("CRASH SMOKE OK user=" + $user + " issue=#" + $issue1 + " report=" + $found.id)
Write-Output ("  created=" + $r1.Status + " deduped=" + $r2.Status + " second=" + $r3.Status + " rate=" + $r4.Status + " oversize=" + $r5.Status)
Write-Output ("  fingerprint=" + $fp1)