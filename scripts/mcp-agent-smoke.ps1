# MCP 与 Agent 运行环境联调：注册全局 MCP Pool → 模板引用 → 部署 Agent 注入令牌 → 读状态。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\mcp-agent-smoke.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$base = "http://127.0.0.1:8092"
$as = "http://127.0.0.1:8766"
$headers = @{ Authorization = "Bearer dev-admin-token" }
$cookie = @{ Cookie = "syntropy_desktop_session=dev-desktop-token" }
$tag = [guid]::NewGuid().ToString("N").Substring(0, 6)
$agentName = "mcp-lead-" + $tag
$project = "prj_sandbox"

function J($o) { $o | ConvertTo-Json -Depth 6 -Compress }

$servers = (Invoke-RestMethod -Uri "$base/admin/runtime/app-servers" -Headers $headers).data
$appServer = ($servers | Where-Object { $_.status -eq "running" } | Select-Object -First 1).id
if (-not $appServer) { throw "没有运行中的 App Server" }
Write-Output ("app_server=" + $appServer)

# 1) 注册全局 MCP Pool 条目
Invoke-RestMethod -Method Post -Uri "$base/admin/runtime/app-servers/$appServer/mcp/servers" -Headers $headers -ContentType "application/json" `
  -Body (J @{ name = "smartpipe"; url = "http://127.0.0.1:8097/mcp"; token_env_var = "SMARTPIPE_MCP_TOKEN" }) | Out-Null
$pool = (Invoke-RestMethod -Uri "$base/admin/runtime/app-servers/$appServer/mcp" -Headers $headers).data
$entry = $pool.servers | Where-Object { $_.name -eq "smartpipe" }
if (-not $entry -or $entry.transport -ne "http" -or $entry.bearer_token_env_var -ne "SMARTPIPE_MCP_TOKEN") {
  throw ("MCP pool entry wrong: " + ($pool | ConvertTo-Json -Depth 6))
}
Write-Output ("pool=" + $entry.name + " " + $entry.transport + " " + $entry.url + " token_env=" + $entry.bearer_token_env_var)

# 2) 模板引用 smartpipe（导入脚本会带上 mcp_config）
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root "scripts\import-template.ps1") -AppServer $appServer | Out-Null
$template = (Invoke-RestMethod -Uri "$base/admin/runtime/app-servers/$appServer/agent-templates/pipeline-lead-v3" -Headers $headers).data
$mcpServers = @($template.settings.mcpConfig.servers)
if ($mcpServers -notcontains "smartpipe") { throw ("template mcpConfig = " + ($mcpServers -join ",")) }
Write-Output ("template.mcp_config.servers=" + ($mcpServers -join ","))

# 3) 部署 Agent，注入 MCP 令牌
$nodes = (Invoke-RestMethod -Uri "$base/admin/runtime/app-servers/$appServer/nodes" -Headers $headers).data
$nodeID = (@($nodes.nodes) | Select-Object -First 1).id
if (-not $nodeID) { throw "没有可用的 Worker 节点" }
Write-Output ("node=" + $nodeID)
$op = (Invoke-RestMethod -Method Post -Uri "$base/admin/runtime/app-servers/$appServer/agents" -Headers $headers -ContentType "application/json" `
  -Body (J @{ name = $agentName; project_id = $project; template_name = "pipeline-lead-v3"; mcp_role = "lead"; target_node_id = $nodeID })).data
$deadline = (Get-Date).AddSeconds(120)
$cur = $op
while ((Get-Date) -lt $deadline) {
  Start-Sleep -Seconds 2
  $cur = (Invoke-RestMethod -Uri "$base/admin/runtime/app-servers/$appServer/agent-operations/$($op.id)" -Headers $headers).data
  $st = [string]$cur.state
  if ($st -in @("4", "5", "6") -or $st -match "SUCCEEDED|FAILED|CANCELED") { break }
}
$st = [string]$cur.state
if ($st -ne "4" -and $st -notmatch "SUCCEEDED") { throw ("deploy failed: state=" + $st + " " + (J $cur)) }
$agentID = if ($cur.agentId) { $cur.agentId } else { $cur.agent_id }
Write-Output ("agent=" + $agentID + " state=" + $cur.state)

$agents = (Invoke-RestMethod -Uri "$base/admin/runtime/app-servers/$appServer/agents" -Headers $headers).data
$agent = $agents | Where-Object { $_.id -eq $agentID } | Select-Object -First 1
$envs = @($agent.spec.environment)
$tokenEnv = $envs | Where-Object { $_.key -eq "SMARTPIPE_MCP_TOKEN" } | Select-Object -First 1
if (-not $tokenEnv -or -not $tokenEnv.value.StartsWith("spt_")) { throw ("agent env missing MCP token: " + (J $agent.spec.environment)) }
if ($agent.spec.labels.'smartpipe.mcp_role' -ne "lead") { throw ("agent labels = " + (J $agent.spec.labels)) }
Write-Output ("agent env SMARTPIPE_MCP_TOKEN=" + $tokenEnv.value.Substring(0, 12) + "… role=" + $agent.spec.labels.'smartpipe.mcp_role')

# 4) 用注入给 Agent 的令牌直接调用 MCP，确认它就是这个 Agent 将来使用的身份
$mcpBody = @{ jsonrpc = "2.0"; id = 1; method = "tools/list" } | ConvertTo-Json -Depth 4 -Compress
$tmp = Join-Path $env:TEMP ("mcp-agent-" + [guid]::NewGuid().ToString("N") + ".json")
[IO.File]::WriteAllText($tmp, $mcpBody, [Text.UTF8Encoding]::new($false))
$raw = & curl.exe -s -m 15 -X POST -H "Content-Type: application/json" -H ("Authorization: Bearer " + $tokenEnv.value) --data-binary "@$tmp" "http://127.0.0.1:8097/mcp"
$listResp = ($raw -join "")
$tools = @($listResp | ConvertFrom-Json).result.tools
if (-not $tools -or $tools.Count -ne 9) { throw ("injected token tools = " + $tools.Count) }
Write-Output ("injected token -> mcp tools=" + $tools.Count + " (verify.patch/derivation.create/release.run present)")

# 5) 清理：删除 Agent 并撤销它的 MCP 令牌
$agentTokenID = $agent.spec.labels.'smartpipe.token_id'
Invoke-RestMethod -Method Delete -Uri "$base/admin/runtime/app-servers/$appServer/agents/$agentID" -Headers $headers | Out-Null
if ($agentTokenID) { Invoke-RestMethod -Method Post -Uri "$base/admin/agent-tokens/$agentTokenID/revoke" -Headers $headers -ContentType "application/json" -Body "{}" | Out-Null }
Write-Output ("agent deleted, token revoked: " + $agentTokenID)

Write-Output ("MCP AGENT SMOKE OK app_server=" + $appServer + " agent=" + $agentName + " tools=" + $tools.Count)