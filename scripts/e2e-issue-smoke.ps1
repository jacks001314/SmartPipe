# 端到端冒烟：跑通一条 Issue 的完整生命周期
#   蓝信消息 → GitHub Issue → dev 无权验证 → /plan 登记方案 → 门禁拦截
#   → /approve v1 → 验证通过 → 提交 → 推送 → Issue 回执 → 打 ready 标签
#   → 发版 dry run 的候选与说明里出现该 Issue。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e-issue-smoke.ps1
param(
  [string]$Base = "http://127.0.0.1:8092",
  [string]$BridgeBase = "http://127.0.0.1:8090",
  [string]$Mcp = "http://127.0.0.1:8097/mcp",
  [string]$FakeGitHub = "http://127.0.0.1:9099",
  [string]$FakeBot = "http://127.0.0.1:9091",
  [string]$AdminToken = "dev-admin-token",
  [string]$BridgeToken = "dev-bridge-token",
  [string]$WebhookSecret = "local-webhook-secret-123"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$stamp = Get-Date -Format "HHmmss"
# 每次运行用独立 Issue 仓库名：假 GitHub 的 /__reset 会把 Issue 编号重置到 100，
# 若沿用同一个仓库名，会撞上数据库里旧 Issue 编号的 /plan 记录，导致 /approve 命中旧方案。
$repoFull = "example/e2e-" + $stamp
$adminHeaders = @{ Authorization = "Bearer $AdminToken" }

# Windows PowerShell 5.1 的 Invoke-WebRequest 按 ISO-8859-1 解码 Content，中文会乱码，
# 这里统一从原始流按 UTF-8 解码。
function Read-Utf8($stream) {
  $ms = New-Object IO.MemoryStream
  $stream.CopyTo($ms)
  return [Text.Encoding]::UTF8.GetString($ms.ToArray())
}

function PostJson($url, $payload, $headers) {
  $json = $payload | ConvertTo-Json -Depth 6 -Compress
  $bytes = [Text.Encoding]::UTF8.GetBytes($json)
  try {
    $resp = Invoke-WebRequest -Uri $url -Method Post -Headers $headers -ContentType "application/json" -Body $bytes -UseBasicParsing -TimeoutSec 120
    $txt = Read-Utf8 $resp.RawContentStream
    $body = $null
    try { $body = $txt | ConvertFrom-Json } catch { }
    return @{ Status = [int]$resp.StatusCode; Text = $txt; Body = $body }
  } catch {
    $r = $_.Exception.Response
    if ($null -eq $r) { throw }
    $txt = Read-Utf8 $r.GetResponseStream()
    $body = $null
    try { $body = $txt | ConvertFrom-Json } catch { }
    return @{ Status = [int]$r.StatusCode; Text = $txt; Body = $body }
  }
}
function GetJson($url, $headers) {
  try {
    $resp = Invoke-WebRequest -Uri $url -Method Get -Headers $headers -UseBasicParsing -TimeoutSec 120
    $txt = Read-Utf8 $resp.RawContentStream
    $body = $null
    try { $body = $txt | ConvertFrom-Json } catch { }
    return @{ Status = [int]$resp.StatusCode; Text = $txt; Body = $body }
  } catch {
    $r = $_.Exception.Response
    if ($null -eq $r) { throw }
    $txt = Read-Utf8 $r.GetResponseStream()
    $body = $null
    try { $body = $txt | ConvertFrom-Json } catch { }
    return @{ Status = [int]$r.StatusCode; Text = $txt; Body = $body }
  }
}

function McpRaw($token, $name, $arguments) {
  return PostJson $Mcp @{ jsonrpc = "2.0"; id = (Get-Random -Maximum 100000); method = "tools/call"; params = @{ name = $name; arguments = $arguments } } @{ Authorization = "Bearer $token" }
}

function McpText($resp) {
  if ($resp.Status -ne 200) { return "" }
  if ($null -eq $resp.Body.result) { return "" }
  $content = $resp.Body.result.content
  if ($null -eq $content) { return "" }
  return [string]$content[0].text
}

function McpTool($token, $name, $arguments) {
  $r = McpRaw $token $name $arguments
  if ($r.Status -ne 200) { throw ($name + " HTTP " + $r.Status + ": " + $r.Text) }
  if ($r.Body.result.isError -eq $true) { throw ($name + " failed: " + (McpText $r)) }
  return ((McpText $r) | ConvertFrom-Json)
}

function Send-Webhook([string]$event, [string]$delivery, $payload) {
  $body = ($payload | ConvertTo-Json -Depth 8 -Compress)
  $bytes = [Text.Encoding]::UTF8.GetBytes($body)
  $hmac = [Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($WebhookSecret))
  $sig = "sha256=" + ([BitConverter]::ToString($hmac.ComputeHash($bytes)) -replace "-", "").ToLower()
  $tmp = Join-Path $env:TEMP ("e2e-wh-" + $delivery + ".json")
  $out = Join-Path $env:TEMP ("e2e-wh-out-" + $delivery + ".json")
  [IO.File]::WriteAllBytes($tmp, $bytes)
  $code = & curl.exe -s -o "$out" -w "%{http_code}" -X POST -H "X-Hub-Signature-256: $sig" -H "X-GitHub-Event: $event" -H "X-GitHub-Delivery: $delivery" -H "Content-Type: application/json" --data-binary "@$tmp" "$BridgeBase/webhooks/github"
  $resp = Get-Content -Raw "$out"
  Remove-Item -LiteralPath $tmp, $out -ErrorAction SilentlyContinue
  return @{ Status = [int]$code; Text = $resp }
}

function Get-LanxinReplies($base) {
  $resp = Invoke-WebRequest -Uri ($base + "/__replies") -UseBasicParsing -TimeoutSec 30
  $txt = Read-Utf8 $resp.RawContentStream
  return @(($txt | ConvertFrom-Json).replies)
}
function Wait-For([scriptblock]$probe, [int]$tries, [int]$sleepMs) {
  for ($i = 0; $i -lt $tries; $i++) {
    $v = & $probe
    if ($v) { return $v }
    Start-Sleep -Milliseconds $sleepMs
  }
  return $null
}
# 1) 项目：Issue 走假 GitHub，代码远端指向本次新建的裸库
$bare = Join-Path $root (".dev\e2e-remote-" + $stamp + ".git")
& git init --bare --quiet $bare | Out-Null
if ($LASTEXITCODE -ne 0) { throw "git init --bare failed" }
$remoteURL = "file:///" + ($bare -replace '\\', '/')
$proj = PostJson ($Base + "/admin/projects") @{
  name = ("e2e-" + $stamp); github_repo_url = $remoteURL; github_issue_repo = $repoFull
  github_default_branch = "master"; labels_prefix = "e2e"; bluex_group_ids = "grp-release"
} $adminHeaders
if ($proj.Status -ne 201) { throw ("create project failed: " + $proj.Status + " " + $proj.Text) }
$projectID = $proj.Body.data.id

$cred = PostJson ($Base + "/admin/credentials") @{
  project_id = $projectID; kind = "github_pat"; name = ("e2e-gh-" + $stamp)
  value = "ghp_e2e_fake_token_123456"; metadata = @{ api_base = $FakeGitHub }
} $adminHeaders
if ($cred.Status -ne 201 -and $cred.Status -ne 200) { throw ("create credential failed: " + $cred.Status + " " + $cred.Text) }
Write-Output ("[1] project=" + $projectID + " remote=" + $remoteURL)

# 2) 蓝信消息 → GitHub Issue
$msgBody = @{ message_id = ("e2e-" + $stamp); chat_id = "grp-release"; user_id = "u-e2e"; title = ("E2E 崩溃 " + $stamp); body = "点开就闪退"; project_id = $projectID } | ConvertTo-Json
$first = Invoke-RestMethod -Method Post -Uri "$BridgeBase/bridge/messages" -Headers @{ "X-Bridge-Token" = $BridgeToken } -ContentType "application/json" -Body $msgBody
$issue = [int]$first.link.issue_number
if (-not $first.created -or $issue -le 0) { throw ("bridge did not create issue: " + ($first | ConvertTo-Json -Compress)) }
Write-Output ("[2] issue=#" + $issue + " dispatch=" + $first.link.dispatch_status)

# 3) 工作副本 + 令牌
$work = Join-Path $root (".dev\e2e-work-" + $stamp)
& git clone --quiet (Join-Path $root ".dev\sandbox-repo") $work
if ($LASTEXITCODE -ne 0) { throw "clone failed" }
# 沙箱仓库没有构建契约，补一个 smartpipe.yaml 并推到裸库 master，
# 让发版 dry run 真的能 clone + build（与 release-smoke 的假构建一致）。
$profileYaml = @"
version: 1
toolchain:
  go: "1.25"
commands:
  build: "git --version > dist.txt"
  unit: "git --version > /dev/null"
required_for_merge:
  - build
timeouts:
  build: 1m
"@
[IO.File]::WriteAllText((Join-Path $work "smartpipe.yaml"), ($profileYaml -replace "`n", "`r`n"), [Text.UTF8Encoding]::new($false))
& git -C $work -c user.name=e2e -c user.email=e2e@example.invalid add smartpipe.yaml
if ($LASTEXITCODE -ne 0) { throw "git add profile failed" }
& git -C $work -c user.name=e2e -c user.email=e2e@example.invalid commit --quiet -m "chore: add smartpipe profile"
if ($LASTEXITCODE -ne 0) { throw "git commit profile failed" }
& git -C $work push --quiet $remoteURL HEAD:refs/heads/master
if ($LASTEXITCODE -ne 0) { throw "git push master failed" }

$baseCommit = (& git -C $work rev-parse HEAD).Trim()
$patch = [string][IO.File]::ReadAllText((Join-Path $root ".dev\patch-multiply.diff"), [Text.UTF8Encoding]::new($false))
$issueRef = $repoFull + "#" + $issue

$leadResp = PostJson ($Base + "/admin/agent-tokens") @{ project_id = $projectID; role = "lead"; name = "e2e-lead" } $adminHeaders
$devResp = PostJson ($Base + "/admin/agent-tokens") @{ project_id = $projectID; role = "dev"; name = "e2e-dev" } $adminHeaders
if ($leadResp.Status -ne 201 -or $devResp.Status -ne 201) { throw ("issue tokens failed: lead=" + $leadResp.Status + " " + $leadResp.Text + " dev=" + $devResp.Status + " " + $devResp.Text) }
$leadToken = $leadResp.Body.data.token
$devToken = $devResp.Body.data.token
$leadTokenID = $leadResp.Body.data.record.id
$devTokenID = $devResp.Body.data.record.id
Write-Output ("[3] base=" + $baseCommit + " tokens=lead+dev")
# 4) dev 令牌无权调用补丁验证（角色裁剪）
$denied = McpRaw $devToken "verify.patch" @{ issue_ref = $issueRef; repo = $work; base_commit = $baseCommit; patch = $patch }
$deniedText = McpText $denied
if ($denied.Body.result.isError -ne $true) { throw ("dev token should not verify: " + $denied.Text) }
if ($deniedText -notmatch "dev") { throw ("unexpected dev denial: " + $deniedText) }
Write-Output ("[4] dev denied: " + $deniedText.Trim())

# 5) lead 在 Issue 里登记方案（webhook 模拟 GitHub 评论）
$planPayload = @{
  action = "created"
  repository = @{ full_name = $repoFull }
  issue = @{ number = $issue; html_url = ($FakeGitHub + "/issues/" + $issue) }
  comment = @{ id = [int](Get-Date -UFormat %s); body = "/plan 1. 复现崩溃；2. 修复并加回归测试；3. 灰度后合并"; author_association = "MEMBER"; user = @{ login = "pipeline-lead" } }
}
$planWh = Send-Webhook "issue_comment" ("e2e-plan-" + $stamp) $planPayload
if ($planWh.Status -ne 202 -and $planWh.Status -ne 200) { throw ("plan webhook http " + $planWh.Status + " " + $planWh.Text) }
$plan = Wait-For {
  $r = GetJson ($Base + "/admin/bridge/plans?limit=50") $adminHeaders
  @($r.Body.data | Where-Object { $_.issue_number -eq $issue -and $_.repo -eq $repoFull }) | Select-Object -First 1
} 50 300
if (-not $plan) { throw "plan not recorded" }
Write-Output ("[5] plan v" + $plan.version + " hash=" + $plan.content_hash)
# 5b. 方案登记要同步到项目绑定的蓝信群
$planNotice = Wait-For {
  $all = Get-LanxinReplies $FakeBot
  $hit = @($all | Where-Object { $_.payload.targetId -eq "grp-release" -and $_.payload.text -match "方案 v1" })
  if ($hit.Count -gt 0) { return $true }
  return $false
} 50 300
if (-not $planNotice) { throw "no lanxin group notice for plan v1" }
Write-Output "[5b] lanxin group notified: plan v1"

# 6) 有方案但没有批准：验证必须被门禁拦住
$before = McpRaw $leadToken "verify.patch" @{ issue_ref = $issueRef; repo = $work; base_commit = $baseCommit; patch = $patch }
$beforeText = McpText $before
if ($before.Body.result.isError -ne $true) { throw ("expected gate rejection, got: " + $before.Text) }
if ($beforeText -notmatch "approval_required") { throw ("gate rejection without approval_required: " + $beforeText) }
Write-Output ("[6] blocked without approval: " + $beforeText.Trim())

# 7) 维护者 /approve v1
$approvePayload = @{
  action = "created"
  repository = @{ full_name = $repoFull }
  issue = @{ number = $issue; html_url = ($FakeGitHub + "/issues/" + $issue) }
  comment = @{ id = [int](Get-Date -UFormat %s); body = ("/approve v" + $plan.version); author_association = "OWNER"; user = @{ login = "maintainer" } }
}
$apWh = Send-Webhook "issue_comment" ("e2e-approve-" + $stamp) $approvePayload
if ($apWh.Status -ne 202 -and $apWh.Status -ne 200) { throw ("approve webhook http " + $apWh.Status + " " + $apWh.Text) }
$approved = Wait-For {
  $st = GetJson ($FakeGitHub + "/__state") @{}
  $iss = @($st.Body.issues | Where-Object { $_.number -eq $issue }) | Select-Object -First 1
  if ($iss) {
    if (@($iss.labels | ForEach-Object { $_.name }) -contains "approved") { return $true }
  }
  return $false
} 50 300
if (-not $approved) { throw "issue not labelled approved after /approve" }
Write-Output "[7] approved label present"
$okNotice = Wait-For {
  $all = Get-LanxinReplies $FakeBot
  $hit = @($all | Where-Object { $_.payload.targetId -eq "grp-release" -and $_.payload.text -match "已获 .* 批准" })
  if ($hit.Count -gt 0) { return $true }
  return $false
} 50 300
if (-not $okNotice) { throw "no lanxin group notice for approval" }
Write-Output "[7b] lanxin group notified: approved"

# 8) 批准后同一个补丁验证通过
$ver = McpTool $leadToken "verify.patch" @{ issue_ref = $issueRef; repo = $work; base_commit = $baseCommit; patch = $patch }
$verJson = $ver | ConvertTo-Json -Depth 6 -Compress
if ($verJson -notmatch '"ok":true') { throw ("verify still failing: " + $verJson) }
$hashMatch = [regex]::Match($verJson, 'sha256:[0-9a-f]+')
Write-Output ("[8] verify ok=true patch_hash=" + $hashMatch.Value)
# 9) lead 提交并推送
$branch = "agent/e2e-" + $stamp
$commit = McpTool $leadToken "repo.commit" @{
  repo = $work; base_commit = $baseCommit; patch = $patch; issue_ref = $issueRef
  message = "fix: add Multiply (e2e)"; branch = $branch; target_branch = ""
}
if ($commit.ok -ne $true) { throw ("commit rejected: " + $commit.error) }
$push = McpTool $leadToken "repo.push" @{ repo = $work; branch = $commit.branch; remote = "origin"; issue_ref = $issueRef }
if ($push.ok -ne $true) { throw ("push rejected: " + $push.error) }
$remoteHead = (& git -C $bare rev-parse ("refs/heads/" + $commit.branch)).Trim()
if ($remoteHead -ne $commit.commit) { throw ("remote head " + $remoteHead + " != " + $commit.commit) }
Write-Output ("[9] push ok branch=" + $commit.branch + " sha=" + $commit.commit)

# 10) Issue 上能看到推送回执
$commented = Wait-For {
  $st = GetJson ($FakeGitHub + "/__state") @{}
  $hit = @($st.Body.comments | Where-Object { $_.issue -eq $issue -and $_.body -match "已推送分支" })
  if ($hit.Count -gt 0) { return $true }
  return $false
} 50 300
if (-not $commented) { throw "issue comment about pushed branch missing" }
Write-Output "[10] issue comment has push receipt"

# 11) 打 ready 标签 → 进入发版候选；dry run 的说明里能看到该 Issue
$null = McpTool $leadToken "issue.add_labels" @{ number = $issue; labels = @("e2e:ready") }
$nodes = GetJson ($Base + "/admin/releases/candidates?project_id=" + $projectID) $adminHeaders
if ($nodes.Status -ne 200) { throw ("candidates http " + $nodes.Status + " " + $nodes.Text) }
$inCandidates = @($nodes.Body.data.candidates | Where-Object { $_.number -eq $issue })
if ($inCandidates.Count -ne 1) { throw ("issue not in candidates: " + $nodes.Text) }

$runVersion = "v9.9." + $stamp
$run = PostJson ($Base + "/admin/releases/runs") @{
  project_id = $projectID; version = $runVersion; branch = $commit.branch; commit = $commit.commit
  dry_run = $true; confirm_major = $true; artifact_globs = @("dist.txt")
} $adminHeaders
if ($run.Status -ne 200) { throw ("release dry run http " + $run.Status + " " + $run.Text) }
if ([string]$run.Text -notmatch ("#" + $issue)) { throw ("release notes missing issue #" + $issue + ": " + $run.Text) }
$runStatus = [string]$run.Body.data.run.status
if ($runStatus -ne "built") { throw ("release dry run should build cleanly, got status=" + $runStatus + " error=" + [string]$run.Body.data.run.error) }
Write-Output ("[11] release dry-run " + $runVersion + " status=" + $runStatus + " candidates=" + @($run.Body.data.run.candidates).Count + " includes #" + $issue + " error=" + [string]$run.Body.data.run.error)

# 12) 清理临时令牌与工作副本
$null = PostJson ($Base + "/admin/agent-tokens/" + $leadTokenID + "/revoke") @{} $adminHeaders
$null = PostJson ($Base + "/admin/agent-tokens/" + $devTokenID + "/revoke") @{} $adminHeaders
Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $bare -Recurse -Force -ErrorAction SilentlyContinue

Write-Output ("E2E ISSUE SMOKE OK project=" + $projectID + " issue=#" + $issue + " commit=" + $commit.commit)