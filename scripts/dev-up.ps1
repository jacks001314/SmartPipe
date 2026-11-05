# 启动配置管理台（本地开发）
#
# 用法：
#   .\scripts\dev-up.ps1              # 读取 .dev/env.ps1 或已有环境变量
#   .\scripts\dev-up.ps1 -Rebuild     # 先构建前端再启动
param(
    [switch]$Rebuild
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".dev\env.ps1"
if (Test-Path -LiteralPath $envFile) {
    . $envFile
}

if (-not $env:SMARTPIPE_DSN) { throw "SMARTPIPE_DSN 未设置：请创建 .dev/env.ps1 或先导出环境变量" }
if (-not $env:SMARTPIPE_MASTER_KEY) { throw "SMARTPIPE_MASTER_KEY 未设置：可用 admin-api -gen-key 生成" }
if (-not $env:SMARTPIPE_ADMIN_ADDR) { $env:SMARTPIPE_ADMIN_ADDR = ":8092" }

$dist = Join-Path $root "web\dist"
if ($Rebuild -or -not (Test-Path -LiteralPath (Join-Path $dist "index.html"))) {
    Push-Location (Join-Path $root "web")
    try {
        if (-not (Test-Path -LiteralPath "node_modules")) { npm install --no-audit --no-fund }
        npm run build
    } finally {
        Pop-Location
    }
}
$env:SMARTPIPE_WEB_DIST = $dist

Push-Location (Join-Path $root "backend")
try {
    Write-Host "admin-api 启动：http://127.0.0.1:8092（Ctrl+C 退出）"
    go run ./cmd/admin-api
} finally {
    Pop-Location
}
