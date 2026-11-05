param(
    [string]$Issue = "111",
    [string]$Repo = "example/core",
    [string]$BridgeBase = "http://127.0.0.1:8090",
    [string]$Secret = "local-webhook-secret-123"
)
$ErrorActionPreference = "Stop"

function Send-Webhook([string]$event, [string]$delivery, $payload) {
    $body = ($payload | ConvertTo-Json -Depth 8 -Compress)
    $bytes = [Text.Encoding]::UTF8.GetBytes($body)
    $hmac = [Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($Secret))
    $sig = "sha256=" + ([BitConverter]::ToString($hmac.ComputeHash($bytes)) -replace "-", "").ToLower()
    $tmp = Join-Path $env:TEMP ("wh-" + $delivery + ".json")
    $out = Join-Path $env:TEMP ("wh-out-" + $delivery + ".json")
    [IO.File]::WriteAllBytes($tmp, $bytes)
    $code = & curl.exe -s -o "$out" -w "%{http_code}" -X POST -H "X-Hub-Signature-256: $sig" -H "X-GitHub-Event: $event" -H "X-GitHub-Delivery: $delivery" -H "Content-Type: application/json" --data-binary "@$tmp" "$BridgeBase/webhooks/github"
    $resp = Get-Content -Raw "$out"
    Write-Output ("[" + $event + "] http=" + $code + " " + $resp.Trim())
    Remove-Item -LiteralPath $tmp, $out -ErrorAction SilentlyContinue
}

$stamp = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
$planText = "/plan 1. reproduce the crash; 2. fix and add regression tests; 3. canary then merge"
$planPayload = @{
    action = "created"
    repository = @{ full_name = $Repo }
    issue = @{ number = [int]$Issue; html_url = "http://127.0.0.1:9099/issues/$Issue" }
    comment = @{ id = $stamp; body = $planText; author_association = "MEMBER"; user = @{ login = "pipeline-lead" } }
}
Send-Webhook "issue_comment" ("plan-" + $stamp) $planPayload

$approvePayload = @{
    action = "created"
    repository = @{ full_name = $Repo }
    issue = @{ number = [int]$Issue; html_url = "http://127.0.0.1:9099/issues/$Issue" }
    comment = @{ id = ($stamp + 1); body = "/approve v1"; author_association = "OWNER"; user = @{ login = "maintainer" } }
}
Send-Webhook "issue_comment" ("approve-" + $stamp) $approvePayload

Write-Output "APPROVAL SMOKE DONE"
