param(
  [string]$Version = "0.1.0",
  [string]$OutputDir = "dist",
  [ValidateSet("windows", "linux", "darwin")]
  [string]$TargetOS = "windows",
  [ValidateSet("amd64", "arm64")]
  [string]$Arch = "amd64",
  [switch]$SkipWeb,
  [switch]$NoRestore,
  [switch]$SkipArchive
)

$ErrorActionPreference = "Stop"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not [IO.Path]::IsPathRooted($OutputDir)) {
  $OutputDir = Join-Path $Root $OutputDir
}
$Out = [IO.Path]::GetFullPath($OutputDir)
$RootPrefix = $Root.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
if (-not $Out.StartsWith($RootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
  throw "OutputDir must stay inside the repository: $Out"
}
if ($Out -eq $Root) {
  throw "OutputDir cannot be the repository root"
}

function Invoke-Checked {
  param(
    [Parameter(Mandatory = $true)][string]$FilePath,
    [Parameter(Mandatory = $true)][string[]]$Arguments,
    [Parameter(Mandatory = $true)][string]$WorkingDirectory
  )
  Push-Location $WorkingDirectory
  try {
    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
      throw "$FilePath exited with code $LASTEXITCODE"
    }
  } finally {
    Pop-Location
  }
}

$Commit = "unknown"
$Dirty = $false
try {
  $Commit = (& git -C $Root rev-parse --short=12 HEAD 2>$null).Trim()
  if ($LASTEXITCODE -ne 0 -or -not $Commit) { $Commit = "unknown" }
  $Dirty = [bool]((& git -C $Root status --porcelain 2>$null) | Select-Object -First 1)
} catch {
  $Commit = "unknown"
}
$BuiltAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")

if (Test-Path -LiteralPath $Out) {
  Remove-Item -LiteralPath $Out -Recurse -Force
}
$BinDir = Join-Path $Out "bin"
New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

$OldGoos = $env:GOOS
$OldGoarch = $env:GOARCH
$OldCgo = $env:CGO_ENABLED
$OldProxy = $env:GOPROXY
try {
  $env:GOOS = $TargetOS
  $env:GOARCH = $Arch
  $env:CGO_ENABLED = "0"
  if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

  if (-not $SkipWeb) {
    $Npm = (Get-Command npm.cmd -ErrorAction SilentlyContinue)
    if (-not $Npm) { $Npm = (Get-Command npm -ErrorAction Stop) }
    if (-not $NoRestore) {
      Invoke-Checked -FilePath $Npm.Source -Arguments @("ci", "--no-audit", "--no-fund") -WorkingDirectory (Join-Path $Root "web")
    }
    Invoke-Checked -FilePath $Npm.Source -Arguments @("run", "build") -WorkingDirectory (Join-Path $Root "web")
  }

  $WebSource = Join-Path $Root "web\dist"
  if (-not (Test-Path -LiteralPath (Join-Path $WebSource "index.html"))) {
    throw "web/dist/index.html is missing; run without -SkipWeb first"
  }
  Copy-Item -LiteralPath $WebSource -Destination (Join-Path $Out "web") -Recurse -Force

  $Ext = ""
  if ($TargetOS -eq "windows") { $Ext = ".exe" }
  $Binaries = [ordered]@{
    "admin-api" = "./cmd/admin-api"
    "bridge" = "./cmd/bridge"
    "patch-verifier" = "./cmd/patch-verifier"
    "scheduler" = "./cmd/scheduler"
    "bot" = "./cmd/bot"
    "botdiag" = "./cmd/botdiag"
    "codex-runner" = "./cmd/codex-runner"
    "codex-pipeline" = "./cmd/codex-pipeline"
  }
  $Ldflags = "-s -w -X main.version=$Version"
  $Go = (Get-Command go -ErrorAction Stop).Source
  $Artifacts = @()
  foreach ($Name in $Binaries.Keys) {
    $Target = Join-Path $BinDir ($Name + $Ext)
    Invoke-Checked -FilePath $Go -Arguments @(
      "build", "-trimpath", "-ldflags", $Ldflags, "-o", $Target, $Binaries[$Name]
    ) -WorkingDirectory (Join-Path $Root "backend")
    $Item = Get-Item -LiteralPath $Target
    $Artifacts += [ordered]@{
      name = $Item.Name
      bytes = $Item.Length
      sha256 = (Get-FileHash -LiteralPath $Target -Algorithm SHA256).Hash.ToLowerInvariant()
    }
  }

  $Configs = Join-Path $Root "configs"
  if (Test-Path -LiteralPath $Configs) {
    Copy-Item -LiteralPath $Configs -Destination (Join-Path $Out "configs") -Recurse -Force
  }
  $Profile = Join-Path $Root "smartpipe.yaml"
  if (Test-Path -LiteralPath $Profile) {
    Copy-Item -LiteralPath $Profile -Destination (Join-Path $Out "smartpipe.yaml") -Force
  }

  $Manifest = [ordered]@{
    name = "SmartPipe"
    version = $Version
    commit = $Commit
    dirty = $Dirty
    built_at = $BuiltAt
    goos = $TargetOS
    goarch = $Arch
    artifacts = $Artifacts
  }
  $Manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $Out "manifest.json") -Encoding UTF8

  $Sums = foreach ($File in Get-ChildItem -LiteralPath $Out -Recurse -File | Where-Object { $_.Name -ne "SHA256SUMS" }) {
    $Rel = $File.FullName.Substring($Out.Length).TrimStart("\").Replace("\", "/")
    (Get-FileHash -LiteralPath $File.FullName -Algorithm SHA256).Hash.ToLowerInvariant() + "  " + $Rel
  }
  $Sums | Set-Content -LiteralPath (Join-Path $Out "SHA256SUMS") -Encoding ASCII

  if (-not $SkipArchive) {
    $Archive = Join-Path $Out ("smartpipe-" + $Version + "-" + $TargetOS + "-" + $Arch + ".zip")
    Compress-Archive -Path (Join-Path $Out "*") -DestinationPath $Archive -CompressionLevel Optimal
    Write-Output ("ARCHIVE " + $Archive)
  }

  $Total = (Get-ChildItem -LiteralPath $Out -Recurse -File | Measure-Object Length -Sum).Sum
  Write-Output ("BUILD OK version=" + $Version + " commit=" + $Commit + " os=" + $TargetOS + "/" + $Arch + " files=" + $Artifacts.Count + " bytes=" + $Total)
  Write-Output ("OUTPUT " + $Out)
} finally {
  $env:GOOS = $OldGoos
  $env:GOARCH = $OldGoarch
  $env:CGO_ENABLED = $OldCgo
  $env:GOPROXY = $OldProxy
}
