# 桥接冒烟：蓝信消息 -> GitHub Issue（可用本地假 GitHub），并验证去重
param(
    [string]$Base = "http://127.0.0.1:8092",
    [string]$BridgeBase = "http://127.0.0.1:8090",
    [string]$Token = "dev-admin-token",
    [string]$BridgeToken = "dev-bridge-token",
    [string]$FakeGitHub = "http://127.0.0.1:9099",
    [string]$FakeBot = "http://127.0.0.1:9091",
    [string]$WebhookSecret = "local-webhook-secret-123",
    [string]$AppServerID = "",
    [string]$AppServerAddr = "http://127.0.0.1:8766",
    [string]$AgentProjectID = "",
    [string]$LeadAgentID = ""
)
$ErrorActionPreference = "Stop"
$headers = @{ Authorization = "Bearer $Token" }
$stamp = Get-Date -Format "HHmmss"

# 1. 项目 + GitHub 凭据（指向假 GitHub）
$project = (Invoke-RestMethod -Method Post -Uri "$Base/admin/projects" -Headers $headers -ContentType "application/json" -Body (@{
    name = "Bridge Demo $stamp"
    github_repo_url = "https://github.com/example/core"
    github_issue_repo = "example/core"
    bluex_group_ids = "grp-bridge"
} | ConvertTo-Json)).data
Write-Output ("project: " + $project.id)

$credBody = @{ project_id = $project.id; kind = "github_pat"; name = "fake-gh-$stamp"; value = "ghp_fake_token_123456"; metadata = @{ api_base = $FakeGitHub } } | ConvertTo-Json -Depth 4
Invoke-RestMethod -Method Post -Uri "$Base/admin/credentials" -Headers $headers -ContentType "application/json" -Body $credBody | Out-Null

if ($LeadAgentID) {
    if (-not $AppServerID) { throw "-LeadAgentID 需要同时给 -AppServerID" }
    $bind = @{ app_server_id = $AppServerID; app_server_addr = $AppServerAddr; agent_project_id = $AgentProjectID; lead_agent_id = $LeadAgentID; lead_agent_name = "smoke-lead" } | ConvertTo-Json
    Invoke-RestMethod -Method Put -Uri "$Base/admin/bridge/bindings/$($project.id)" -Headers $headers -ContentType "application/json" -Body $bind | Out-Null
    Write-Output ("binding: lead=" + $LeadAgentID)
}

# 2. 同一个 message_id 投两次（用增量判断，假 GitHub 可能已有历史数据）
$beforeCount = @((Invoke-RestMethod -Uri "$FakeGitHub/__state").issues).Count
$msg = @{ message_id = "msg-$stamp"; chat_id = "grp-bridge"; user_id = "u-demo"; title = "启动崩溃 $stamp"; body = "点开就闪退"; project_id = $project.id } | ConvertTo-Json
$first = Invoke-RestMethod -Method Post -Uri "$BridgeBase/bridge/messages" -Headers (@{ "X-Bridge-Token" = $BridgeToken }) -ContentType "application/json" -Body $msg
Write-Output ("first: created=" + $first.created + " issue=" + $first.link.issue_number + " dispatch=" + $first.link.dispatch_status)
if ($LeadAgentID -and $first.link.dispatch_status -ne "sent") { throw ("expected dispatch sent, got " + $first.link.dispatch_status + " " + $first.link.dispatch_error) }
if (-not $LeadAgentID -and $first.link.dispatch_status -ne "unbound") { throw ("expected unbound without lead binding, got " + $first.link.dispatch_status) }
$second = Invoke-RestMethod -Method Post -Uri "$BridgeBase/bridge/messages" -Headers (@{ "X-Bridge-Token" = $BridgeToken }) -ContentType "application/json" -Body $msg
Write-Output ("second: created=" + $second.created + " issue=" + $second.link.issue_number)
if ($second.created) { throw "duplicate message created a second issue" }

# 2b. 建单回执必须回到原会话（假 bot 记录 /internal/reply）
$receipt = $null
for ($i = 0; $i -lt 30; $i++) {
  $all = @((Invoke-RestMethod -Uri "$FakeBot/__replies").replies)
  $hit = @($all | Where-Object { $_.payload.refMsgId -eq ("msg-" + $stamp) })
  if ($hit.Count -ge 1) { $receipt = $hit[0]; break }
  Start-Sleep -Milliseconds 200
}
if ($null -eq $receipt) { throw ("no lanxin receipt for msg-" + $stamp) }
$allReceipts = @((Invoke-RestMethod -Uri "$FakeBot/__replies").replies)
$mine = @($allReceipts | Where-Object { $_.payload.refMsgId -eq ("msg-" + $stamp) })
if ($mine.Count -ne 1) { throw ("expected exactly 1 receipt, got " + $mine.Count) }
$expectIssue = "Issue #" + $first.link.issue_number
if ($receipt.payload.text -notmatch [regex]::Escape($expectIssue)) { throw ("receipt missing " + $expectIssue + ": " + $receipt.payload.text) }
if ($receipt.payload.text -notmatch [regex]::Escape([string]$first.link.issue_url)) { throw ("receipt missing issue url: " + $receipt.payload.text) }
if ($receipt.payload.targetId -ne "grp-bridge") { throw ("receipt went to wrong target: " + $receipt.payload.targetId) }
Write-Output ("receipt: refMsgId=msg-" + $stamp + " issue=#" + $first.link.issue_number + " target=" + $receipt.payload.targetId)

$issues = @((Invoke-RestMethod -Uri "$FakeGitHub/__state").issues)
$delta = $issues.Count - $beforeCount
Write-Output ("github issues delta: " + $delta)
if ($delta -ne 1) { throw "expected exactly 1 new GitHub issue, got $delta" }

# 3. Webhook 验签 + 去重
$body = "{""action"":""opened"",""repository"":{""full_name"":""example/core""},""issue"":{""number"":$($first.link.issue_number)}}"
$bytes = [Text.Encoding]::UTF8.GetBytes($body)
$hmac = [Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($WebhookSecret))
$sig = "sha256=" + ([BitConverter]::ToString($hmac.ComputeHash($bytes)) -replace "-", "").ToLower()
$whHeaders = @{ "X-Hub-Signature-256" = $sig; "X-GitHub-Event" = "issues"; "X-GitHub-Delivery" = "delivery-$stamp" }
# Invoke-WebRequest 在 PS 5.1 下处理原始字节 Body 会抛 NullReferenceException，改用 curl.exe
$tmpBody = Join-Path $env:TEMP "wh-body-$stamp.json"
[IO.File]::WriteAllBytes($tmpBody, $bytes)
$c1 = & curl.exe -s -o "$env:TEMP\wh-r1-$stamp.json" -w "%{http_code}" -X POST -H "X-Hub-Signature-256: $sig" -H "X-GitHub-Event: issues" -H "X-GitHub-Delivery: delivery-$stamp" -H "Content-Type: application/json" --data-binary "@$tmpBody" "$BridgeBase/webhooks/github"
$c2 = & curl.exe -s -o "$env:TEMP\wh-r2-$stamp.json" -w "%{http_code}" -X POST -H "X-Hub-Signature-256: $sig" -H "X-GitHub-Event: issues" -H "X-GitHub-Delivery: delivery-$stamp" -H "Content-Type: application/json" --data-binary "@$tmpBody" "$BridgeBase/webhooks/github"
$r2 = Get-Content -Raw "$env:TEMP\wh-r2-$stamp.json"
Remove-Item -LiteralPath $tmpBody, "$env:TEMP\wh-r1-$stamp.json", "$env:TEMP\wh-r2-$stamp.json" -ErrorAction SilentlyContinue
Write-Output ("webhook: first=" + $c1 + " second=" + $c2 + " " + $r2.Trim())
if ($c1 -notin @("200","202")) { throw "first webhook status $c1" }
if ($c2 -notin @("200","202")) { throw "second webhook status $c2" }
if ($r2 -notmatch "duplicate") { throw "second delivery not deduped" }

# 4. 映射表
$links = @((Invoke-RestMethod -Uri "$BridgeBase/bridge/links").data)
Write-Output ("bridge links: " + $links.Count)
Write-Output "BRIDGE SMOKE OK"
