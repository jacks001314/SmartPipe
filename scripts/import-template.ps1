# 把 configs/agents/<role>/AGENTS.md 导入沙箱 Master 的 Agent 模板库
param(
    [string]$Role = "pipeline-lead",
    [string]$TemplateName = "pipeline-lead-v3",
    [string]$AppServer = "",
    [string]$Base = "http://127.0.0.1:8092",
    [string]$Token = "dev-admin-token"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$file = Join-Path $root "configs\agents\$Role\AGENTS.md"
if (-not (Test-Path -LiteralPath $file)) { throw "missing $file" }
# 用 .NET 读取 UTF-8 原文，避免 PowerShell 5.1 的默认编码把中文读坏。
$content = [IO.File]::ReadAllText($file, [Text.Encoding]::UTF8)

$headers = @{ Authorization = "Bearer $Token" }
if (-not $AppServer) {
    $servers = (Invoke-RestMethod -Uri "$Base/admin/runtime/app-servers" -Headers $headers).data
    $running = $servers | Where-Object { $_.status -eq "running" } | Select-Object -First 1
    if (-not $running) { throw "no running app server; start one first" }
    $AppServer = $running.id
}

$body = @{
    template_name = $TemplateName
    display_name  = "SmartPipe $Role"
    agent_type    = "codex-native"
    agent_role    = $Role
    agent_emoji   = "🧭"
    description   = "SmartPipe $Role template (imported from configs/agents/$Role)"
    markdown_files = @{ "AGENTS.md" = $content }
} | ConvertTo-Json -Depth 6

$res = Invoke-RestMethod -Method Post -Uri "$Base/admin/runtime/app-servers/$AppServer/agent-templates" -Headers $headers -ContentType "application/json" -Body ([Text.Encoding]::UTF8.GetBytes($body))
Write-Output ("template " + $TemplateName + " -> " + ($res.data.status | Out-String).Trim())
