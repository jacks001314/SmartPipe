# smartpipe-mcp 联调：真实 admin-api + 真实 Postgres + 假 GitHub + 补丁验证服务。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\mcp-smoke.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$api = "http://127.0.0.1:8092/admin"
$mcp = "http://127.0.0.1:8097/mcp"
$gh = "http://127.0.0.1:9099"
$project = "prj_5148b505"
$adminHeaders = @{ Authorization = "Bearer dev-admin-token" }

function PostJson($url, $payload, $headers) {
  $tmp = Join-Path $env:TEMP ("sp-mcp-" + [guid]::NewGuid().ToString("N") + ".json")
  [IO.File]::WriteAllText($tmp, ($payload | ConvertTo-Json -Depth 4 -Compress), [Text.UTF8Encoding]::new($false))
  $argv = @("-s", "-m", "120", "-o", "$tmp.out", "-w", "%{http_code}", "-X", "POST", "-H", "Content-Type: application/json")
  foreach ($k in $headers.Keys) { $argv += @("-H", ($k + ": " + $headers[$k])) }
  $argv += @("--data-binary", "@$tmp", $url)
  $status = & curl.exe @argv
  $text = [IO.File]::ReadAllText("$tmp.out", [Text.UTF8Encoding]::new($false))
  return @{ Status = [int]$status; Text = $text; Body = ($text | ConvertFrom-Json) }
}

function Rpc($token, $method, $params) {
  $payload = @{ jsonrpc = "2.0"; id = (Get-Random -Maximum 100000); method = $method }
  if ($null -ne $params) { $payload.params = $params }
  return PostJson $mcp $payload @{ Authorization = "Bearer $token" }
}

function Tool($token, $name, $arguments) {
  $r = Rpc $token "tools/call" @{ name = $name; arguments = $arguments }
  if ($r.Status -ne 200) { throw ("tools/call HTTP " + $r.Status + ": " + $r.Text) }
  return $r.Body.result
}

# 1) 管理员签发 lead 令牌
$created = PostJson ($api + "/agent-tokens") @{ project_id = $project; role = "lead"; name = "smoke-lead" } $adminHeaders
if ($created.Status -ne 201) { throw ("create token failed: " + $created.Status + " " + $created.Text) }
$leadToken = $created.Body.data.token
$leadID = $created.Body.data.record.id
if (-not $leadToken.StartsWith("spt_")) { throw "token format unexpected" }

# 2) initialize / tools/list
$init = Rpc $leadToken "initialize" @{ protocolVersion = "2025-06-18"; capabilities = @{}; clientInfo = @{ name = "smoke"; version = "1" } }
if ($init.Status -ne 200 -or $init.Body.result.protocolVersion -ne "2025-06-18") { throw ("initialize failed: " + $init.Text) }
$list = Rpc $leadToken "tools/list" $null
$toolNames = @($list.Body.result.tools | ForEach-Object { $_.name })
if ($toolNames.Count -ne 9) { throw ("lead tools = " + ($toolNames -join ",")) }
foreach ($need in @("project.info", "issue.comment", "verify.patch", "derivation.create", "release.run", "release.rollback")) {
  if ($toolNames -notcontains $need) { throw ("missing tool " + $need) }
}

# 3) project.info
$info = Tool $leadToken "project.info" @{ project_id = $project }
$infoText = $info.content[0].text | ConvertFrom-Json
if ($infoText.name -ne "release-demo") { throw ("project.info = " + $info.content[0].text) }

Write-Output 'STEP 4 candidates'
# 4) release.candidates
$cands = Tool $leadToken "release.candidates" @{}
if ($null -eq $cands.content) { throw "candidates failed" }

Write-Output 'STEP 5 issue.comment'
# 5) issue.comment 写到假 GitHub
$comment = Tool $leadToken "issue.comment" @{ number = 101; body = "MCP smoke: plan v1 draft" }
if ($comment.isError -eq $true) { throw ("comment error: " + $comment.content[0].text) }
$state = Invoke-RestMethod -Uri ($gh + "/__state")
$hit = @($state.comments | Where-Object { $_.issue -eq 101 -and $_.body -like "MCP smoke:*" })
if ($hit.Count -lt 1) { throw "github comment not found" }

Write-Output 'STEP 6 verify.patch'
# 6) verify.patch（干净副本 + smartpipe.yaml）
$patch = [string][IO.File]::ReadAllText((Join-Path $root ".dev\patch-multiply.diff"), [Text.UTF8Encoding]::new($false))
$verify = Tool $leadToken "verify.patch" @{
  issue_ref = "example/release#101"; repo = (Join-Path $root ".dev\sandbox-repo")
  base_commit = "ddac226063b9631e05dc752dc319e73a5f1683c5"; patch = $patch; author = "mcp-smoke"
}
$verifyText = $verify.content[0].text
$verifyBody = $verifyText | ConvertFrom-Json
if ($verify.isError -eq $true) { throw ("verify error: " + $verifyText) }
if ($verifyBody.report.ok -ne $true) { throw ("verify report not ok: " + $verifyText) }

# 7) dev 角色只能看到 project.info，且不能派生
$devCreated = PostJson ($api + "/agent-tokens") @{ project_id = $project; role = "dev"; name = "smoke-dev" } $adminHeaders
$devToken = $devCreated.Body.data.token
$devList = Rpc $devToken "tools/list" $null
$devTools = @($devList.Body.result.tools | ForEach-Object { $_.name })
if ($devTools.Count -ne 1 -or $devTools[0] -ne "project.info") { throw ("dev tools = " + ($devTools -join ",")) }
$denied = Tool $devToken "derivation.create" @{ issue_ref = "example/release#101"; agent_name = "dev-x" }
if ($denied.isError -ne $true) { throw "dev must not create derivations" }

# 8) 撤销令牌后拒绝
$revoke = PostJson ($api + "/agent-tokens/" + $leadID + "/revoke") @{} $adminHeaders
if ($revoke.Status -ne 200) { throw ("revoke failed: " + $revoke.Text) }
$after = Rpc $leadToken "tools/list" $null
if ($after.Status -ne 401) { throw ("revoked token status = " + $after.Status) }

Write-Output ("MCP SMOKE OK project=" + $project + " lead_token=" + $leadID)
Write-Output ("  tools=" + ($toolNames -join ","))
Write-Output ("  verify_report_ok=" + $verifyBody.report.ok + " patch_hash=" + $verifyBody.report.patch_hash)
Write-Output ("  dev_tools=" + ($devTools -join ",") + " revoked_status=" + $after.Status)