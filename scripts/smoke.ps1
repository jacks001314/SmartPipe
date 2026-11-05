# 配置管理台冒烟测试：项目 -> 凭据 -> 连接测试 -> 审计
param(
    [string]$Base = "http://127.0.0.1:8092",
    [string]$Token = "dev-admin-token"
)
$ErrorActionPreference = "Stop"
$headers = @{}
if ($Token) { $headers["Authorization"] = "Bearer $Token" }

$stamp = Get-Date -Format "HHmmss"
$body = @{
    name = "Smoke Project $stamp"
    description = "smoke test"
    github_repo_url = "https://github.com/example/smoke"
    github_issue_repo = "example/smoke"
    bluex_group_ids = "grp-smoke"
} | ConvertTo-Json
$project = (Invoke-RestMethod -Method Post -Uri "$Base/admin/projects" -Headers $headers -ContentType "application/json" -Body $body).data
Write-Output ("project: " + $project.id + " / " + $project.name)

$credBody = @{
    project_id = $project.id
    kind = "github_webhook_secret"
    name = "smoke-webhook-$stamp"
    value = "smoke-secret-value-1234567890"
    metadata = @{ note = "smoke" }
} | ConvertTo-Json -Depth 4
$cred = (Invoke-RestMethod -Method Post -Uri "$Base/admin/credentials" -Headers $headers -ContentType "application/json" -Body $credBody).data
Write-Output ("credential: " + $cred.id + " mask=" + $cred.mask)

$test = (Invoke-RestMethod -Method Post -Uri ("$Base/admin/credentials/" + $cred.id + "/test") -Headers $headers).data
Write-Output ("test: ok=" + $test.ok + " message=" + $test.message)

$list = (Invoke-RestMethod -Uri ("$Base/admin/credentials?project_id=" + $project.id) -Headers $headers).data
if ($list.Count -ne 1) { throw "expected 1 credential, got $($list.Count)" }
if ($list[0].mask -ne "****7890") { throw "unexpected mask: $($list[0].mask)" }

$audit = (Invoke-RestMethod -Uri "$Base/admin/audit?limit=20" -Headers $headers).data
Write-Output ("audit entries: " + $audit.Count)
if ($audit.Count -lt 3) { throw "expected >=3 audit entries" }

Write-Output "SMOKE OK"
