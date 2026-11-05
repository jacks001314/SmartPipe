# 部署 pipeline-lead 并轮询 AgentOperation 直到终态
param(
    [string]$AppServer = "",
    [string]$Project = "prj_sandbox",
    [string]$Name = "lead-sandbox-01",
    [string]$Template = "pipeline-lead-v3",
    [string]$Node = "",
    [string]$Base = "http://127.0.0.1:8092",
    [string]$Token = "dev-admin-token",
    [int]$TimeoutSec = 120
)
$ErrorActionPreference = "Stop"
$headers = @{ Authorization = "Bearer $Token" }
if (-not $AppServer) {
    $servers = (Invoke-RestMethod -Uri "$Base/admin/runtime/app-servers" -Headers $headers).data
    $running = $servers | Where-Object { $_.status -eq "running" } | Select-Object -First 1
    if (-not $running) { throw "no running app server" }
    $AppServer = $running.id
}
$payload = @{
    name = $Name
    project_id = $Project
    template_name = $Template
}
if ($Node) { $payload.target_node_id = $Node }
$op = (Invoke-RestMethod -Method Post -Uri "$Base/admin/runtime/app-servers/$AppServer/agents" -Headers $headers -ContentType "application/json" -Body ($payload | ConvertTo-Json -Depth 4)).data
Write-Output ("operation " + $op.id + " state=" + $op.state)

$deadline = (Get-Date).AddSeconds($TimeoutSec)
while ((Get-Date) -lt $deadline) {
    Start-Sleep -Seconds 2
    $cur = (Invoke-RestMethod -Uri "$Base/admin/runtime/app-servers/$AppServer/agent-operations/$($op.id)" -Headers $headers).data
    $state = [string]$cur.state
    Write-Output ("  state=" + $state + " err=" + $cur.errorMessage)
    if ($state -match "SUCCEEDED|FAILED|CANCELED") { break }
}
Write-Output ("agent_id=" + $cur.agentId)
$agents = (Invoke-RestMethod -Uri "$Base/admin/runtime/app-servers/$AppServer/agents" -Headers $headers).data
$agents | Where-Object { $_.id -eq $cur.agentId } | ConvertTo-Json -Depth 3
