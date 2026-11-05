# Lanxin one-click crash upload SIT: fake lanxin(:9092) + isolated bot(:18093) + bridge ingest(:8090).
# Prereq: node scripts/fake-lanxin.mjs 9092. Use -UseExistingBot only for a fake bot on -BotPort.
param(
  [int]$BotPort = 18093,
  [switch]$UseExistingBot
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$bot = "http://127.0.0.1:$BotPort"
$lanxin = "http://127.0.0.1:9092"
$smokeBot = $null
$savedEnv = @{}
if (-not $UseExistingBot) {
  $exe = Join-Path $root "backend\bin\bot.exe"
  if (-not (Test-Path -LiteralPath $exe)) { throw "missing backend/bin/bot.exe" }
  foreach ($key in @("LANXIN_APP_ID", "LANXIN_APP_SECRET", "LANXIN_API_BASE", "BOT_HTTP_ADDR", "INTERNAL_TOKEN", "CRASH_INGEST_URL", "CRASH_INGEST_TOKEN")) {
    $savedEnv[$key] = [Environment]::GetEnvironmentVariable($key, "Process")
  }
  $env:LANXIN_APP_ID = "fake-app"
  $env:LANXIN_APP_SECRET = "fake-secret"
  $env:LANXIN_API_BASE = $lanxin
  $env:BOT_HTTP_ADDR = ":$BotPort"
  $env:INTERNAL_TOKEN = "dev-internal-token"
  $env:CRASH_INGEST_URL = "http://127.0.0.1:8090/ingest/crash"
  $env:CRASH_INGEST_TOKEN = "dev-crash-token"
  $slug = [guid]::NewGuid().ToString("N")
  $log = Join-Path $root (".dev\bot-smoke-" + $slug + ".log")
  $errLog = Join-Path $root (".dev\bot-smoke-" + $slug + ".err.log")
  $smokeBot = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $errLog -PassThru
  $ready = $false
  for ($i = 0; $i -lt 30; $i++) {
    try { Invoke-RestMethod -Uri ($bot + "/healthz") -TimeoutSec 2 | Out-Null; $ready = $true; break } catch { Start-Sleep -Milliseconds 200 }
  }
  if (-not $ready) {
    Get-Content -LiteralPath $errLog -ErrorAction SilentlyContinue
    if (-not $smokeBot.HasExited) { Stop-Process -Id $smokeBot.Id -Force }
    throw "isolated smoke bot failed to start on $BotPort"
  }
}
try {

function Send-Event($payload) {
  $tmp = Join-Path $env:TEMP ("sp-bot-" + [guid]::NewGuid().ToString("N") + ".json")
  [IO.File]::WriteAllText($tmp, ($payload | ConvertTo-Json -Depth 6 -Compress), [Text.UTF8Encoding]::new($false))
  $status = & curl.exe -s -o "$tmp.out" -w "%{http_code}" -X POST -H "Content-Type: application/json" -H "X-Internal-Token: dev-internal-token" --data-binary "@$tmp" ($bot + "/internal/events")
  if ([int]$status -ne 200) { throw ("inject failed: " + $status + " " + [IO.File]::ReadAllText("$tmp.out")) }
}
function LastReply() { return (Invoke-RestMethod -Uri ($lanxin + "/__messages")).sent[-1] }
function ReplyText($r) { return [string]$r.payload.msgData.formatText.text }

# 自备项目：绑定 grp-release，让 bridge 按群反查到真实项目
$adminBase = "http://127.0.0.1:8092/admin"
$gh = "http://127.0.0.1:9099"
$adminHeaders = @{ Authorization = "Bearer dev-admin-token" }
$stampG = [guid]::NewGuid().ToString("N").Substring(0, 6)
$groupID = "grp-bot-" + $stampG
$proj = (Invoke-RestMethod -Method Post -Uri ($adminBase + "/projects") -Headers $adminHeaders -ContentType "application/json" -Body (@{
  name = ("bot-crash-" + $stampG); github_repo_url = "https://github.com/example/release"; github_issue_repo = "example/release"
  github_default_branch = "master"; labels_prefix = "smartpipe"; bluex_group_ids = $groupID; enabled = $true
} | ConvertTo-Json)).data
Invoke-RestMethod -Method Post -Uri ($adminBase + "/credentials") -Headers $adminHeaders -ContentType "application/json" -Body (@{ project_id = $proj.id; kind = "github_pat"; name = "fake-gh"; value = "fake-token"; metadata = @{ api_base = $gh } } | ConvertTo-Json -Depth 4) | Out-Null
$null = & curl.exe -s -X POST ($lanxin + "/__reset")
$mediaDir = Join-Path $root ".dev\lanxin-media"
New-Item -ItemType Directory -Force -Path $mediaDir | Out-Null

$runUser = "u-smoke-" + [DateTime]::UtcNow.ToString("HHmmss")
$bundle = @{
  app = "desktop-client"; version = "4.2.1"; os = "Windows 11 23H2"; arch = "amd64"
  user_id = $runUser; occurred_at = "2026-11-05T06:50:00Z"
  exception = @{
    type = "NullReferenceException"; message = "smoke crash upload " + $runUser
    stack = ("at Login.Form.OnSubmit(" + $runUser + ") in C:\work\Login.cs:line 88`nat Main.Run() in C:\work\Main.cs:line 12")
  }
} | ConvertTo-Json -Depth 5
[IO.File]::WriteAllText((Join-Path $mediaDir "smoke-bundle"), $bundle, [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText((Join-Path $mediaDir "smoke-big"), ('{"app":"x","exception":{"type":"Big","stack":"' + ('x' * 300000) + '"}}'), [Text.UTF8Encoding]::new($false))

$stamp = [DateTime]::UtcNow.ToString("HHmmss")
function Event($msgID, $mediaID) {
  return @{
    type = "bot_group_message"
    data = @{
      msgType = "text"; chatType = "group"; from = "u-10293"; senderName = "smoke"; msgId = $msgID
      groupId = $groupID
      msgData = @{ text = @{ content = ""; mediaType = 3; mediaIds = @($mediaID) } }
      reminder = @{ isAtMe = $true }
    }
  }
}

Send-Event (Event ("smoke-1-" + $stamp) "smoke-bundle")
Start-Sleep -Milliseconds 900
$t1 = ReplyText (LastReply)
if ($t1 -notmatch "issues/\d+") { throw ("create reply = " + $t1) }
Write-Output ("STEP 1 created -> " + $t1)

Send-Event (Event ("smoke-2-" + $stamp) "smoke-big")
Start-Sleep -Milliseconds 900
$t2 = ReplyText (LastReply)
if ($t2 -notmatch "262144") { throw ("oversize reply = " + $t2) }
Write-Output ("STEP 2 oversized rejected -> " + $t2)

Send-Event (Event ("smoke-3-" + $stamp) "smoke-bundle")
Start-Sleep -Milliseconds 900
$t3 = ReplyText (LastReply)
if ($t3 -notmatch "Issue #") { throw ("dedupe reply = " + $t3) }
Write-Output ("STEP 3 deduped -> " + $t3)

Send-Event @{ type = "bot_message"; data = @{ msgType = "text"; chatType = "p2p"; from = "u-10293"; msgId = ("smoke-help-" + $stamp); msgData = @{ text = @{ content = "/crash" } } } }
Start-Sleep -Milliseconds 700
$t4 = ReplyText (LastReply)
if ($t4 -notmatch "crash_bundle") { throw ("help reply = " + $t4) }
Write-Output "STEP 4 /crash help ok"

$total = (Invoke-RestMethod -Uri ($lanxin + "/__messages")).sent.Count
Write-Output ("BOT CRASH SMOKE OK replies=" + $total)
} finally {
  if ($smokeBot -and -not $smokeBot.HasExited) { Stop-Process -Id $smokeBot.Id -Force }
  foreach ($key in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($key, $savedEnv[$key], "Process") }
}
