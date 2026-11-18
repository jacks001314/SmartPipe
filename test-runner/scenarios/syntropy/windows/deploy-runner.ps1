<#
.SYNOPSIS
  Prepare a Windows desktop test machine and register the interactive worker task.

Run this on the test machine after copying test-runner next to it (or pass
-SourceRunner). The worker must run inside the logged-on desktop session, so it
is started through a scheduled task with LogonType Interactive.
#>
param(
  [Parameter(Mandatory=$true)][string]$InstallRoot,
  [Parameter(Mandatory=$true)][string]$ApiURL,
  [Parameter(Mandatory=$true)][string]$MachineID,
  [Parameter(Mandatory=$true)][string]$Token,
  [string]$Base = 'C:\SmartPipeTest',
  [string]$ScenarioName = 'syntropy-desktop-hotswap',
  [string]$ScenarioVersion = '1',
  [string]$NodeExe = 'C:\Program Files\nodejs\node.exe',
  [string]$SourceRunner = (Join-Path $PSScriptRoot '..\..\..'),
  [ValidateSet('Limited','Highest')][string]$RunLevel = 'Limited',
  [string]$TunnelHost = '',
  [string]$TunnelUser = 'ubuntu',
  [int]$TunnelLocalPort = 8092,
  [int]$TunnelRemotePort = 8092,
  [string]$SshKey = (Join-Path $env:USERPROFILE '.ssh\id_ed25519'),
  [string]$SshExe = (Join-Path $env:SystemRoot 'System32\OpenSSH\ssh.exe'),
  # Keep the worker resident and polling instead of claiming exactly once.
  [switch]$Loop,
  [int]$IntervalSec = 20,
  # Start the resident worker automatically when the test user logs on.
  [switch]$AtLogOn,
  # Agent session host: shares the desktop mutex and local lease with the worker.
  [switch]$InstallSessionHost,
  [string[]]$SessionEnvironments = @('syntropy'),
  [string[]]$SessionRoots = @()
)
$ErrorActionPreference = 'Stop'
function Fail([string]$message) { [Console]::Error.WriteLine($message); exit 1 }

if (-not (Test-Path -LiteralPath (Join-Path $InstallRoot 'Syntropy.exe'))) { Fail "Syntropy.exe not found under $InstallRoot" }
if (-not (Test-Path -LiteralPath $NodeExe)) { Fail "node.exe not found: $NodeExe" }
if ($Loop -and ($IntervalSec -lt 5 -or $IntervalSec -gt 3600)) { Fail 'IntervalSec must be 5..3600' }
if ($TunnelHost) {
  $api = [uri]$ApiURL
  if ($api.Scheme -ne 'http' -or @('127.0.0.1','localhost','::1') -notcontains $api.Host -or $api.Port -ne $TunnelLocalPort) {
    Fail "-ApiURL must be http://127.0.0.1:$TunnelLocalPort when -TunnelHost is used"
  }
  if (-not (Test-Path -LiteralPath $SshExe)) { Fail "ssh.exe not found: $SshExe" }
  if (-not (Test-Path -LiteralPath $SshKey)) { Fail "SSH private key not found: $SshKey" }
}
$runner = Join-Path $Base 'runner'
$state = Join-Path $Base 'state'
$artifacts = Join-Path $Base 'artifacts'
$scripts = Join-Path $Base 'scripts'
foreach ($dir in @($runner,$state,$artifacts,$scripts)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }

Copy-Item -Recurse -Force -Path (Join-Path $SourceRunner 'dist') -Destination $runner
Copy-Item -Force -Path (Join-Path $SourceRunner 'package.json') -Destination $runner
Copy-Item -Recurse -Force -Path (Join-Path $SourceRunner 'scenarios') -Destination $runner
$ctl = Join-Path $scripts 'syntropy-ctl.ps1'
Copy-Item -Force -Path (Join-Path $PSScriptRoot 'syntropy-ctl.ps1') -Destination $ctl
Copy-Item -Force -Path (Join-Path $PSScriptRoot 'run-worker.ps1') -Destination (Join-Path $Base 'run-worker.ps1')
Copy-Item -Force -Path (Join-Path $PSScriptRoot 'run-session-host.ps1') -Destination (Join-Path $Base 'run-session-host.ps1')
Copy-Item -Force -Path (Join-Path $PSScriptRoot '..\..\windows\capture-window.ps1') -Destination (Join-Path $scripts 'capture-window.ps1')

$tokenFile = Join-Path $Base 'machine.token'
[System.IO.File]::WriteAllText($tokenFile, $Token, (New-Object System.Text.UTF8Encoding($false)))
$identity = "$env:USERDOMAIN\$env:USERNAME"
& icacls.exe $tokenFile /inheritance:r /grant:r "${identity}:(F)" | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "Failed to restrict machine token ACL: $tokenFile" }

$ps = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$ctlHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $ctl).Hash.ToLowerInvariant()
function New-Step([string]$id,[string]$action,[int]$timeout = 60000,[hashtable]$extra = @{},[string[]]$extraArgs = @()) {
  $step = [ordered]@{ id = $id; executable = $ps;
    args = @('-NoProfile','-ExecutionPolicy','Bypass','-File',$ctl,'-Action',$action) + $extraArgs; timeoutMs = $timeout }
  foreach ($key in $extra.Keys) { $step[$key] = $extra[$key] }
  return $step
}
$config = [ordered]@{
  apiURL = $ApiURL
  machineID = $MachineID
  tunnel = if ($TunnelHost) { [ordered]@{
    host = $TunnelHost; user = $TunnelUser; localPort = $TunnelLocalPort
    remotePort = $TunnelRemotePort; sshKey = $SshKey; sshExe = $SshExe
  } } else { $null }
  bundleScenario = [ordered]@{
    name = $ScenarioName
    version = $ScenarioVersion
    stateRoot = $state
    workingDirectory = $Base
    bundlePath = (Join-Path $artifacts 'candidate.tar.gz')
    installRoot = $InstallRoot
    scripts = @(@{ path = $ctl; sha256 = $ctlHash })
    stop = @(New-Step 'stop_app' 'stop' 120000)
    start = @(New-Step 'start_app' 'start' 180000)
    checks = @(
      (New-Step 'app_swapped' 'verify-swap' 120000),
      (New-Step 'app_running' 'status'),
      (New-Step 'app_window' 'wait' 180000 @{} @('-SettleSec','25')),
      (New-Step 'app_version' 'expect-version' 60000),
      (New-Step 'nbot_smoke' 'nbot-smoke' 60000),
      (New-Step 'app_screenshot' 'screenshot' 60000 @{ evidenceFile = 'syntropy-main.png'; evidenceMime = 'image/png' })
    )
    cleanup = @(New-Step 'expect_stopped' 'expect-stopped' 60000)
  }
}
$configPath = Join-Path $Base 'worker.json'
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 10), (New-Object System.Text.UTF8Encoding($false)))

if ($SessionRoots.Count -eq 0) {
  $SessionRoots = @($Base, $InstallRoot,
    (Join-Path $env:USERPROFILE '.syntropy'),
    (Join-Path $env:APPDATA 'Syntropy'),
    (Join-Path $env:LOCALAPPDATA 'Syntropy'))
}
$sessionConfig = [ordered]@{
  apiURL = $ApiURL
  machineID = $MachineID
  stateRoot = $state
  workingDirectory = $Base
  environments = $SessionEnvironments
  allowedRoots = @($SessionRoots | Where-Object { $_ } | Select-Object -Unique)
  execAllowlist = @(
    (Join-Path $env:SystemRoot 'System32'),
    'C:\Program Files\nodejs',
    $InstallRoot
  )
  screenshotScript = (Join-Path $scripts 'capture-window.ps1')
  maxCommandMs = 600000
}
$sessionConfigPath = Join-Path $Base 'session.json'
[System.IO.File]::WriteAllText($sessionConfigPath, ($sessionConfig | ConvertTo-Json -Depth 10), (New-Object System.Text.UTF8Encoding($false)))
$config.sessionConfig = $sessionConfigPath
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 10), (New-Object System.Text.UTF8Encoding($false)))

$runWorker = Join-Path $Base 'run-worker.ps1'
$workerArgs = '-NoProfile -ExecutionPolicy Bypass -File "' + $runWorker + '" -Config "' + $configPath + '"'
if ($Loop) { $workerArgs += ' -Loop -IntervalSec ' + $IntervalSec }
$action = New-ScheduledTaskAction -Execute $ps -Argument $workerArgs
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel $RunLevel
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
$register = @{ TaskName = 'SmartPipeTestRunner'; Action = $action; Principal = $principal; Settings = $settings; Force = $true }
if ($AtLogOn) { $register.Trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME" }
Register-ScheduledTask @register | Out-Null

if ($InstallSessionHost) {
  $runHost = Join-Path $Base 'run-session-host.ps1'
  $hostArgs = '-NoProfile -ExecutionPolicy Bypass -File "' + $runHost + '" -Config "' + $sessionConfigPath + '" -Loop -IntervalSec 5'
  $hostAction = New-ScheduledTaskAction -Execute $ps -Argument $hostArgs
  $hostRegister = @{ TaskName = 'SmartPipeTestSessionHost'; Action = $hostAction; Principal = $principal; Settings = $settings; Force = $true }
  if ($AtLogOn) { $hostRegister.Trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME" }
  Register-ScheduledTask @hostRegister | Out-Null
}

[pscustomobject]@{ config = $configPath; session_config = $sessionConfigPath; task = 'SmartPipeTestRunner'; session_task = $(if ($InstallSessionHost) { 'SmartPipeTestSessionHost' } else { '' }); ctl = $ctl; ctl_sha256 = $ctlHash; bundle = (Join-Path $artifacts 'candidate.tar.gz'); loop = [bool]$Loop; interval_sec = $IntervalSec; at_logon = [bool]$AtLogOn } | ConvertTo-Json
if ($Loop) {
  Write-Host 'The worker now polls until stopped: schtasks /run /tn SmartPipeTestRunner'
} else {
  Write-Host 'Start a run with: schtasks /run /tn SmartPipeTestRunner'
}
