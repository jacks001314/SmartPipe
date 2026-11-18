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
  try { $graphics.CopyFromScreen($bounds.Left, $bounds.Top, 0, 0, $bitmap.Size) }
  finally { $graphics.Dispose() }
  try { Save-Bitmap $bitmap } finally { $bitmap.Dispose() }
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
