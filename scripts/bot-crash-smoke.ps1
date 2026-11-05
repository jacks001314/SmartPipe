# Lanxin one-click crash upload SIT: fake lanxin(:9092) + bot(:8093) + bridge ingest(:8090).
# Prereq: node scripts/fake-lanxin.mjs 9092 and scripts/dev-run-bot.ps1 are running.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$bot = "http://127.0.0.1:8093"
$lanxin = "http://127.0.0.1:9092"

function Send-Event($payload) {
  $tmp = Join-Path $env:TEMP ("sp-bot-" + [guid]::NewGuid().ToString("N") + ".json")
  [IO.File]::WriteAllText($tmp, ($payload | ConvertTo-Json -Depth 6 -Compress), [Text.UTF8Encoding]::new($false))
  $status = & curl.exe -s -o "$tmp.out" -w "%{http_code}" -X POST -H "Content-Type: application/json" -H "X-Internal-Token: dev-internal-token" --data-binary "@$tmp" ($bot + "/internal/events")
  if ([int]$status -ne 200) { throw ("inject failed: " + $status + " " + [IO.File]::ReadAllText("$tmp.out")) }
}
function LastReply() { return (Invoke-RestMethod -Uri ($lanxin + "/__messages")).sent[-1] }
function ReplyText($r) { return [string]$r.payload.msgData.formatText.text }

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
      groupId = "grp-release"
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
