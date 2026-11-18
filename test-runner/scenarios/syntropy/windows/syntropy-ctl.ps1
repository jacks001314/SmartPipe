<#
.SYNOPSIS
  Control an installed Syntropy desktop instance for hot-swap acceptance runs.

Every action either exits 0 after verifying the fact or exits non-zero with a
reason on stderr. The screenshot action uses window-level PrintWindow first, so
it still works when the interactive desktop is locked; it falls back to a
screen grab when PrintWindow is unavailable.
#>
param(
  [Parameter(Mandatory=$true)]
  [ValidateSet('status','stop','start','wait','screenshot','version','expect-version','expect-stopped','verify-swap','nbot-smoke')]
  [string]$Action,
  [string]$Root = $env:SMARTPIPE_SYNTROPY_ROOT,
  [string]$OutPath = $(if ($env:SMARTPIPE_TEST_OUTPUT) { Join-Path $env:SMARTPIPE_TEST_OUTPUT 'syntropy-main.png' } else { '' }),
  [int]$TimeoutSec = 90,
  [int]$SettleSec = 8,
  [string]$WindowTitle = 'Syntropy',
  [string]$Expect = $env:SMARTPIPE_EXPECT_VERSION
)
$ErrorActionPreference = 'Stop'

function Fail([int]$code, [string]$message) {
  [Console]::Error.WriteLine($message)
  exit $code
}
if ([string]::IsNullOrWhiteSpace($Root)) {
  Fail 2 'Root is required (pass -Root or set SMARTPIPE_SYNTROPY_ROOT)'
}
$exe = Join-Path $Root 'Syntropy.exe'

Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class SyntropyWin {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int cmd);
  [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hWnd, IntPtr hdcBlt, uint flags);
}
"@

function Get-TargetProcesses {
  $all = @(Get-CimInstance Win32_Process)
  $ids = New-Object 'System.Collections.Generic.HashSet[int]'
  $rootPrefix = $Root.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
  foreach ($p in $all) {
    $path = $p.ExecutablePath
    if ($path -and ($path -ieq $exe -or $path.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase))) {
      [void]$ids.Add([int]$p.ProcessId)
    }
  }
  if ($ids.Count -gt 0) {
    $changed = $true
    while ($changed) {
      $changed = $false
      foreach ($p in $all) {
        if ($ids.Contains([int]$p.ParentProcessId) -and -not $ids.Contains([int]$p.ProcessId)) {
          [void]$ids.Add([int]$p.ProcessId)
          $changed = $true
        }
      }
    }
  }
  return @($all | Where-Object { $ids.Contains([int]$_.ProcessId) })
}
function Stop-Targets {
  $procs = Get-TargetProcesses
  foreach ($p in $procs) { try { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue } catch {} }
  if ($procs.Count -gt 0) { Start-Sleep -Milliseconds 800 }
  $left = Get-TargetProcesses
  foreach ($p in $left) { try { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue } catch {} }
  if ($left.Count -gt 0) { Start-Sleep -Milliseconds 800 }
  $still = Get-TargetProcesses
  if ($still.Count -gt 0) {
    Fail 3 ('Syntropy processes are still running: ' + (($still | ForEach-Object { "$($_.Name):$($_.ProcessId)" }) -join ','))
  }
}
function Find-Window {
  return @(Get-Process | Where-Object { $_.MainWindowHandle -ne 0 -and $_.MainWindowTitle -match $WindowTitle } | Select-Object -First 1)
}
function Restore-Window([System.Diagnostics.Process]$proc) {
  $handle = $proc.MainWindowHandle
  if ($handle -eq 0) { return $null }
  $rect = New-Object SyntropyWin+RECT
  [void][SyntropyWin]::GetWindowRect($handle, [ref]$rect)
  $width = $rect.Right - $rect.Left
  if ([SyntropyWin]::IsIconic($handle) -or $width -lt 400 -or $rect.Left -le -30000) {
    [void][SyntropyWin]::ShowWindow($handle, 9)
    $deadline = (Get-Date).AddSeconds(8)
    while ((Get-Date) -lt $deadline) {
      [void][SyntropyWin]::GetWindowRect($handle, [ref]$rect)
      $width = $rect.Right - $rect.Left
      if (-not [SyntropyWin]::IsIconic($handle) -and $width -ge 400 -and $rect.Left -gt -30000) { break }
      Start-Sleep -Milliseconds 200
    }
  }
  if ($width -lt 400 -or $rect.Left -le -30000) { return $null }
  return $handle
}
function Wait-RestoredWindow {
  $deadline = (Get-Date).AddSeconds($TimeoutSec)
  while ((Get-Date) -lt $deadline) {
    $proc = Find-Window
    if ($proc) {
      $handle = Restore-Window $proc
      if ($handle) { return @{ Process = $proc; Handle = $handle } }
    }
    Start-Sleep -Milliseconds 500
  }
  return $null
}
function Test-BlankBitmap([System.Drawing.Bitmap]$bitmap) {
  $colors = New-Object 'System.Collections.Generic.HashSet[int]'
  $stepX = [Math]::Max(1, [int]($bitmap.Width / 32))
  $stepY = [Math]::Max(1, [int]($bitmap.Height / 32))
  for ($x = 0; $x -lt $bitmap.Width; $x += $stepX) {
    for ($y = 0; $y -lt $bitmap.Height; $y += $stepY) {
      [void]$colors.Add($bitmap.GetPixel($x, $y).ToArgb())
    }
  }
  return ($colors.Count -lt 4)
}
function Get-ProductVersion {
  $manifest = Join-Path $Root 'resources/release-manifest.json'
  if (Test-Path -LiteralPath $manifest) {
    $value = (Get-Content -LiteralPath $manifest -Raw | ConvertFrom-Json).productVersion
    if ($value) { return [string]$value }
  }
  if (Test-Path -LiteralPath $exe) { return [string](Get-Item -LiteralPath $exe).VersionInfo.ProductVersion }
  return $null
}

switch ($Action) {
  'status' {
    $procs = Get-TargetProcesses
    $proc = Find-Window
    [pscustomobject]@{
      running = ($procs.Count -gt 0)
      pids = @($procs | ForEach-Object { [int]$_.ProcessId })
      exe = $exe
      window = if ($proc) { $proc.MainWindowTitle } else { $null }
    } | ConvertTo-Json -Compress
    if ($procs.Count -eq 0) { exit 1 }
    exit 0
  }
  'stop' {
    Stop-Targets
    exit 0
  }
  'expect-stopped' {
    $procs = Get-TargetProcesses
    if ($procs.Count -gt 0) {
      Fail 10 ('Syntropy is still running: ' + (($procs | ForEach-Object { "$($_.Name):$($_.ProcessId)" }) -join ','))
    }
    exit 0
  }
  'start' {
    if (-not (Test-Path -LiteralPath $exe)) { Fail 4 ("Syntropy.exe not found: " + $exe) }
    Stop-Targets
    Start-Process -FilePath $exe -WorkingDirectory $Root | Out-Null
    $window = Wait-RestoredWindow
    if (-not $window) { Fail 5 'Syntropy window did not appear before the timeout' }
    exit 0
  }
  'wait' {
    $window = Wait-RestoredWindow
    if (-not $window) { Fail 5 'Syntropy window did not appear before the timeout' }
    Start-Sleep -Seconds $SettleSec
    $window = Wait-RestoredWindow
    if (-not $window) { Fail 6 'Syntropy window disappeared while settling' }
    exit 0
  }
  'version' {
    $value = Get-ProductVersion
    if ([string]::IsNullOrWhiteSpace($value)) { Fail 6 'Cannot determine the installed version' }
    [pscustomobject]@{ version = $value } | ConvertTo-Json -Compress
    exit 0
  }
  'expect-version' {
    if ([string]::IsNullOrWhiteSpace($Expect)) { Fail 7 'Expect is required (pass -Expect or set SMARTPIPE_EXPECT_VERSION)' }
    $value = Get-ProductVersion
    if ([string]::IsNullOrWhiteSpace($value)) { Fail 6 'Cannot determine the installed version' }
    if ($value -ne $Expect) { Fail 8 ("installed version " + $value + ' != expected ' + $Expect) }
    exit 0
  }
  'verify-swap' {
    if ([string]::IsNullOrWhiteSpace($env:SMARTPIPE_TEST_OUTPUT)) { Fail 11 'SMARTPIPE_TEST_OUTPUT is required' }
    $manifestPath = Join-Path $env:SMARTPIPE_TEST_OUTPUT 'bundle/bundle.manifest.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { Fail 12 ("bundle manifest not found: " + $manifestPath) }
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.format -ne 1 -or -not $manifest.components) { Fail 13 'unsupported bundle manifest' }
    $results = @()
    foreach ($component in $manifest.components) {
      $relative = [string]$component.target
      if ([System.IO.Path]::IsPathRooted($relative) -or $relative -match '(^|[\\/])\.\.([\\/]|$)') {
        $results += [pscustomobject]@{ target = $relative; ok = $false; error = 'invalid target' }
        continue
      }
      $targetPath = Join-Path $Root $relative
      if (-not (Test-Path -LiteralPath $targetPath -PathType Leaf)) {
        $results += [pscustomobject]@{ target = $relative; ok = $false; error = 'missing' }
        continue
      }
      $actual = (Get-FileHash -LiteralPath $targetPath -Algorithm SHA256).Hash.ToLowerInvariant()
      $results += [pscustomobject]@{
        target = $relative; expected = [string]$component.sha256; actual = $actual
        ok = ($actual -eq [string]$component.sha256)
      }
    }
    $results | ConvertTo-Json -Compress
    if (@($results | Where-Object { -not $_.ok }).Count -gt 0) { exit 14 }
    exit 0
  }
  'screenshot' {
    if ([string]::IsNullOrWhiteSpace($OutPath)) { Fail 9 'OutPath is required' }
    Add-Type -AssemblyName System.Drawing
    $window = Wait-RestoredWindow
    if (-not $window) { Fail 5 'Syntropy window did not appear before the timeout' }
    $handle = $window.Handle
    [void][SyntropyWin]::SetForegroundWindow($handle)
    Start-Sleep -Milliseconds 500
    $rect = New-Object SyntropyWin+RECT
    if (-not [SyntropyWin]::GetWindowRect($handle, [ref]$rect)) { Fail 15 'GetWindowRect failed' }
    $width = $rect.Right - $rect.Left
    $height = $rect.Bottom - $rect.Top
    if ($width -le 0 -or $height -le 0) { Fail 15 'Syntropy window has no drawable area' }
    $directory = [System.IO.Path]::GetDirectoryName($OutPath)
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    $bitmap = New-Object System.Drawing.Bitmap $width, $height
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $dc = $graphics.GetHdc()
    $printed = $false
    try { $printed = [SyntropyWin]::PrintWindow($handle, $dc, 2) } finally { $graphics.ReleaseHdc($dc) }
    if ($printed -and -not (Test-BlankBitmap $bitmap)) {
      $bitmap.Save($OutPath, [System.Drawing.Imaging.ImageFormat]::Png)
      $graphics.Dispose(); $bitmap.Dispose()
      exit 0
    }
    $graphics.Dispose(); $bitmap.Dispose()
    try {
      Add-Type -AssemblyName System.Windows.Forms
      $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
      $bitmap = New-Object System.Drawing.Bitmap $bounds.Width, $bounds.Height
      $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
      $graphics.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
      $bitmap.Save($OutPath, [System.Drawing.Imaging.ImageFormat]::Png)
      $graphics.Dispose(); $bitmap.Dispose()
      exit 0
    } catch {
      Fail 16 ('screen capture failed (PrintWindow=' + $printed + '): ' + $_.Exception.Message)
    }
  }
  'nbot-smoke' {
    $nbot = Join-Path $Root 'resources/node/artifacts/nbot-windows-amd64.exe'
    if (-not (Test-Path -LiteralPath $nbot -PathType Leaf)) { Fail 17 ("nbot not found: " + $nbot) }
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $nbot
    $start.Arguments = 'dns'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $proc = [System.Diagnostics.Process]::Start($start)
    $stdout = $proc.StandardOutput.ReadToEndAsync()
    $stderr = $proc.StandardError.ReadToEndAsync()
    if (-not $proc.WaitForExit(30000)) { try { $proc.Kill() } catch {}; Fail 18 'nbot smoke test timed out' }
    $text = ($stdout.Result + $stderr.Result).Trim()
    [pscustomobject]@{ exit = $proc.ExitCode; output = $text } | ConvertTo-Json -Compress
    if ($proc.ExitCode -ne 0) { exit 19 }
    exit 0
  }
}
