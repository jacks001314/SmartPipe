param(
  [Parameter(Mandatory=$true)][string]$OutPath,
  [string]$Title = '',
  [int]$TimeoutSec = 20,
  [switch]$FullScreen
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class SmartPipeCapture {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hWnd, IntPtr hdcBlt, uint flags);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int cmd);
  [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr extra);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowTextLength(IntPtr hWnd);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, System.Text.StringBuilder text, int count);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetClassName(IntPtr hWnd, System.Text.StringBuilder text, int count);
  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr extra);
}
"@

function Save-Bitmap([System.Drawing.Bitmap]$bitmap) {
  $dir = Split-Path -Parent $OutPath
  if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
  $bitmap.Save($OutPath, [System.Drawing.Imaging.ImageFormat]::Png)
  Write-Output $OutPath
}

function Capture-FullScreen {
  Add-Type -AssemblyName System.Windows.Forms
  $bounds = [System.Windows.Forms.SystemInformation]::VirtualScreen
  $bitmap = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $copied = $false
  try { $graphics.CopyFromScreen($bounds.Left, $bounds.Top, 0, 0, $bitmap.Size); $copied = $true }
  catch { }
  finally { $graphics.Dispose() }
  if (-not $copied) {
    # CopyFromScreen needs a rendering desktop. A disconnected RDP session or a
    # locked screen produces "invalid handle"; PrintWindow still renders a
    # window from its own surface, so fall back to the largest visible window.
    $bitmap.Dispose()
    $handle = Get-LargestWindow
    if ($handle -eq [IntPtr]::Zero -or -not (Capture-WindowHandle $handle)) {
      [Console]::Error.WriteLine("screen capture failed: no usable desktop session and no capturable window. " +
        "Keep the RDP/console session connected and unlocked while a test needs screenshots.")
      exit 3
    }
    Write-Output "fallback: captured largest visible window"
    return
  }
  try { Save-Bitmap $bitmap } finally { $bitmap.Dispose() }
}

function Get-WindowTitle([IntPtr]$handle) {
  $length = [SmartPipeCapture]::GetWindowTextLength($handle)
  if ($length -le 0) { return '' }
  $buffer = New-Object System.Text.StringBuilder ($length + 1)
  [void][SmartPipeCapture]::GetWindowText($handle, $buffer, $buffer.Capacity)
  return $buffer.ToString()
}

function Get-WindowClass([IntPtr]$handle) {
  $buffer = New-Object System.Text.StringBuilder 256
  [void][SmartPipeCapture]::GetClassName($handle, $buffer, $buffer.Capacity)
  return $buffer.ToString()
}

function Get-LargestWindow {
  $best = [IntPtr]::Zero
  $bestArea = 0
  $callback = [SmartPipeCapture+EnumWindowsProc]{
    param([IntPtr]$handle, [IntPtr]$extra)
    if ([SmartPipeCapture]::IsWindowVisible($handle) -and (Get-WindowTitle $handle)) {
      $rect = New-Object SmartPipeCapture+RECT
      if ([SmartPipeCapture]::GetWindowRect($handle, [ref]$rect)) {
        $area = ($rect.Right - $rect.Left) * ($rect.Bottom - $rect.Top)
        # Shell surfaces (desktop, input experience) and terminals capture as a
        # solid black frame, so they are only a last resort.
        $class = Get-WindowClass $handle
        $blacklist = @('Progman','WorkerW','Shell_TrayWnd','Windows.UI.Core.CoreWindow',
          'ConsoleWindowClass','CASCADIA_HOSTING_WINDOW_CLASS','ApplicationManager_DesktopShellWindow')
        $score = if ($blacklist -contains $class) { 0 } else { 1 }
        # Non-shell windows always outrank shell surfaces; a shell window is
        # still chosen when it is the only thing on screen.
        $effective = if ($score -eq 1) { $area } else { 1 }
        if ($effective -gt $script:bestArea) { $script:bestArea = $effective; $script:best = $handle }
      }
    }
    return $true
  }
  [void][SmartPipeCapture]::EnumWindows($callback, [IntPtr]::Zero)
  return $script:best
}

function Capture-WindowHandle([IntPtr]$handle) {
  $rect = New-Object SmartPipeCapture+RECT
  if (-not [SmartPipeCapture]::GetWindowRect($handle, [ref]$rect)) { return $false }
  $width = $rect.Right - $rect.Left
  $height = $rect.Bottom - $rect.Top
  if ($width -lt 50 -or $height -lt 50) { return $false }
  if ([SmartPipeCapture]::IsIconic($handle)) { [void][SmartPipeCapture]::ShowWindow($handle, 9); Start-Sleep -Milliseconds 400 }
  $bitmap = New-Object System.Drawing.Bitmap($width, $height)
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $hdc = $graphics.GetHdc()
  try { $ok = [SmartPipeCapture]::PrintWindow($handle, $hdc, 2) }
  finally { $graphics.ReleaseHdc($hdc); $graphics.Dispose() }
  if (-not $ok) { $bitmap.Dispose(); return $false }
  try { Save-Bitmap $bitmap } finally { $bitmap.Dispose() }
  return $true
}

if ($FullScreen -or [string]::IsNullOrWhiteSpace($Title)) {
  Capture-FullScreen
  exit 0
}

$deadline = (Get-Date).AddSeconds($TimeoutSec)
$proc = $null
while ((Get-Date) -lt $deadline) {
  $proc = Get-Process | Where-Object { $_.MainWindowHandle -ne 0 -and $_.MainWindowTitle -match $Title } | Select-Object -First 1
  if ($proc) { break }
  Start-Sleep -Milliseconds 300
}
if (-not $proc) {
  [Console]::Error.WriteLine("No window matching '$Title' within $TimeoutSec seconds")
  exit 2
}
$handle = $proc.MainWindowHandle
if ([SmartPipeCapture]::IsIconic($handle)) { [void][SmartPipeCapture]::ShowWindow($handle, 9); Start-Sleep -Milliseconds 500 }
[void][SmartPipeCapture]::SetForegroundWindow($handle)
Start-Sleep -Milliseconds 300
$rect = New-Object SmartPipeCapture+RECT
[void][SmartPipeCapture]::GetWindowRect($handle, [ref]$rect)
$width = $rect.Right - $rect.Left
$height = $rect.Bottom - $rect.Top
if ($width -lt 50 -or $height -lt 50) {
  [Console]::Error.WriteLine("Window '$Title' has no capturable area")
  exit 3
}
$bitmap = New-Object System.Drawing.Bitmap($width, $height)
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$hdc = $graphics.GetHdc()
try { $ok = [SmartPipeCapture]::PrintWindow($handle, $hdc, 2) }
finally { $graphics.ReleaseHdc($hdc); $graphics.Dispose() }
if (-not $ok) {
  $bitmap.Dispose()
  Capture-FullScreen
  exit 0
}
try { Save-Bitmap $bitmap } finally { $bitmap.Dispose() }
