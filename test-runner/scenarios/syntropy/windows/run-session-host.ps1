param(
  [Parameter(Mandatory=$true)][string]$Config,
  [string]$RunnerRoot = 'C:\SmartPipeTest\runner',
  [string]$TokenFile = 'C:\SmartPipeTest\machine.token',
  [string]$NodeExe = 'C:\Program Files\nodejs\node.exe',
  [string]$LogFile = 'C:\SmartPipeTest\session-host.log',
  [switch]$Loop,
  [int]$IntervalSec = 5
)
$ErrorActionPreference = 'Stop'
if ($Loop -and ($IntervalSec -lt 5 -or $IntervalSec -gt 3600)) { throw 'IntervalSec must be 5..3600' }

# One desktop owner per machine: the deterministic worker and the session host
# share this mutex, and the local desktop lease is the second line of defence.
$mutex = New-Object System.Threading.Mutex($false, 'Local\SmartPipeTestRunner')
$ownsMutex = $false
try { $ownsMutex = $mutex.WaitOne(0) } catch [System.Threading.AbandonedMutexException] { $ownsMutex = $true }
if (-not $ownsMutex) {
  Write-Host 'Another SmartPipeTestRunner instance is already active'
  $mutex.Dispose()
  exit 0
}
Set-Location -LiteralPath $RunnerRoot
if (-not (Test-Path -LiteralPath $TokenFile)) { throw "Machine token file is missing: $TokenFile" }
$env:SMARTPIPE_TEST_MACHINE_TOKEN = (Get-Content -LiteralPath $TokenFile -Raw).Trim()
$cli = Join-Path $RunnerRoot 'dist/src/session-host-cli.js'
if (-not (Test-Path -LiteralPath $cli)) { throw "session-host-cli.js is missing: $cli" }

function Test-LocalPort([int]$Port) {
  $client = New-Object System.Net.Sockets.TcpClient
  try {
    $task = $client.ConnectAsync('127.0.0.1', $Port)
    return ($task.Wait(500) -and $client.Connected)
  } catch { return $false } finally { $client.Dispose() }
}

$tunnel = $null
$exitCode = 1
if ($LogFile) { Start-Transcript -Path $LogFile -Append | Out-Null }
try {
  $settings = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
  if ($settings.tunnel -and $settings.tunnel.host) {
    $t = $settings.tunnel
    if (-not (Test-LocalPort ([int]$t.localPort))) {
      if (-not (Test-Path -LiteralPath $t.sshExe)) { throw "ssh.exe not found: $($t.sshExe)" }
      if (-not (Test-Path -LiteralPath $t.sshKey)) { throw "SSH private key not found: $($t.sshKey)" }
      $forward = "127.0.0.1:$($t.localPort):127.0.0.1:$($t.remotePort)"
      $sshArgs = @('-i', $t.sshKey, '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes',
        '-o', 'StrictHostKeyChecking=accept-new', '-o', 'ServerAliveInterval=30',
        '-o', 'ServerAliveCountMax=3', '-o', 'ExitOnForwardFailure=yes', '-N',
        '-L', $forward, "$($t.user)@$($t.host)")
      Write-Host "Starting SSH tunnel $forward via $($t.user)@$($t.host)"
      $tunnel = Start-Process -FilePath $t.sshExe -ArgumentList $sshArgs -WindowStyle Hidden -PassThru
      $deadline = (Get-Date).AddSeconds(20)
      while ((Get-Date) -lt $deadline -and -not (Test-LocalPort ([int]$t.localPort))) {
        if ($tunnel.HasExited) { throw "SSH tunnel exited with code $($tunnel.ExitCode)" }
        Start-Sleep -Milliseconds 250
      }
      if (-not (Test-LocalPort ([int]$t.localPort))) { throw 'SSH tunnel did not become ready' }
    }
  }
  do {
    & $NodeExe $cli $Config
    $exitCode = $LASTEXITCODE
    if ($Loop) {
      Write-Host "Session host iteration finished with exit code $exitCode; polling again in $IntervalSec second(s)"
      Start-Sleep -Seconds $IntervalSec
    }
  } while ($Loop)
} finally {
  if ($tunnel -and -not $tunnel.HasExited) { Stop-Process -Id $tunnel.Id -Force -ErrorAction SilentlyContinue }
  if ($LogFile) { Stop-Transcript | Out-Null }
  if ($ownsMutex) { $mutex.ReleaseMutex() }
  $mutex.Dispose()
}
exit $exitCode
