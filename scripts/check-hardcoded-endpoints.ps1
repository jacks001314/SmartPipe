# 静态检查：产品代码、部署模板与脚本中不得写死环境私有地址。
# 真实地址必须来自当前环境的 .env、安装参数或控制台录入。
# 只扫描产品与模板目录；docs/ 与 reports/ 中的历史联调记录不在范围内。
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\check-hardcoded-endpoints.ps1
param(
  [string]$Root = ""
)
$ErrorActionPreference = "Stop"
if (-not $Root) { $Root = Split-Path -Parent $PSScriptRoot }
$Root = (Resolve-Path -LiteralPath $Root).Path

$scanDirs = @("backend", "web\src", "deploy", "scripts", "configs", "sdks", "test-runner\src", "test-runner\test")
$binaryExtensions = @(".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".webp", ".zip", ".gz", ".tar", ".exe", ".dll", ".so", ".dylib", ".pdf", ".woff", ".woff2", ".ttf", ".otf", ".mp4", ".lock")
# override 文件本身就是"当前环境"的真实地址，只校验 example 模板。
$skipNames = @("check-hardcoded-endpoints.ps1", "docker-compose.override.yml")
$skipPatterns = @('_test\.go$', '\.test\.tsx?$', '\.spec\.tsx?$')

$ipv4Pattern = '(?<![\d.])(?:\d{1,3}\.){3}\d{1,3}(?![\d.])'
# 仅允许进程绑定/回环地址；其他 IPv4 字面量一律视为环境相关配置。
$allowedIPv4 = @('0.0.0.0', '127.0.0.1', '255.255.255.255')

$findings = New-Object System.Collections.ArrayList
foreach ($dir in $scanDirs) {
  $dirPath = Join-Path $Root $dir
  if (-not (Test-Path -LiteralPath $dirPath)) { continue }
  Get-ChildItem -LiteralPath $dirPath -Recurse -File | ForEach-Object {
    $file = $_
    if ($file.FullName -match '[\\/](node_modules|dist|\.git)[\\/]') { return }
    if ($skipNames -contains $file.Name) { return }
    foreach ($skip in $skipPatterns) { if ($file.Name -match $skip) { return } }
    if ($binaryExtensions -contains $file.Extension.ToLowerInvariant()) { return }
    if ($file.Length -gt 1MB) { return }
    $lines = @(Get-Content -LiteralPath $file.FullName -Encoding UTF8 -ErrorAction SilentlyContinue)
    if ($null -eq $lines) { return }
    $relative = $file.FullName.Substring($Root.Length + 1)
    for ($i = 0; $i -lt $lines.Count; $i++) {
      foreach ($ip in [regex]::Matches($lines[$i], $ipv4Pattern)) {
        if ($allowedIPv4 -contains $ip.Value) { continue }
        $null = $findings.Add(("{0}:{1}: [IPv4 literal] {2}" -f $relative, ($i + 1), $lines[$i].Trim()))
        break
      }
    }
  }
}

if ($findings.Count -gt 0) {
  Write-Output "[FAIL] 发现环境地址字面量；请改为环境变量、安装参数或控制台录入："
  $findings | ForEach-Object { Write-Output ("  " + $_) }
  exit 1
}
Write-Output "[PASS] 未发现环境地址字面量"
