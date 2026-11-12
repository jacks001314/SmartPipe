# 由计划任务调用：加载 executor.env 后启动 release-executor，输出追加到日志。
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$envPath = Join-Path $root 'executor.env'
$exe = Join-Path $root 'bin\release-executor.exe'
$logDir = Join-Path $root 'logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$logFile = Join-Path $logDir 'release-executor.log'

# 简单轮转：超过 10MB 时换名保留一份。
if ((Test-Path -LiteralPath $logFile) -and ((Get-Item -LiteralPath $logFile).Length -gt 10MB)) {
    Move-Item -LiteralPath $logFile -Destination ($logFile + '.1') -Force
}

foreach ($line in Get-Content -LiteralPath $envPath) {
    if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
    $name, $value = $line -split '=', 2
    Set-Item -Path ("Env:" + $name.Trim()) -Value $value.TrimEnd()
}

# 计划任务以 SYSTEM 运行，PATH 里通常没有 git / signtool，按需追加。
$extraPaths = @(
    'C:\Program Files\Git\cmd',
    'C:\Program Files (x86)\Windows Kits\10\bin\x64'
)
foreach ($p in $extraPaths) {
    if ((Test-Path -LiteralPath $p) -and ($env:PATH -notlike "*$p*")) {
        $env:PATH = $p + ';' + $env:PATH
    }
}

"[$(Get-Date -Format o)] starting release-executor" | Out-File -LiteralPath $logFile -Append -Encoding utf8
& $exe *>&1 | Out-File -LiteralPath $logFile -Append -Encoding utf8
$code = $LASTEXITCODE
"[$(Get-Date -Format o)] release-executor exited with $code" | Out-File -LiteralPath $logFile -Append -Encoding utf8
exit $code