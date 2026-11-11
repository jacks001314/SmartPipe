param(
  [string]$HostName = "192.168.1.99",
  [string]$User = "jacks",
  [ValidateRange(1, 65535)]
  [int]$Port = 22,
  [string]$PassFile = "",
  [string]$EnvFile = "",
  [ValidateSet("docker", "binary")]
  [string]$Mode = "docker",
  [string]$InstallDir = "/opt/smartpipe",
  [string]$Version = "0.1.0",
  [string]$BundleDir = "",
  [switch]$SkipBuild,
  [switch]$NoSudo,
  [switch]$SkipWebBuild
)

$ErrorActionPreference = "Stop"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$Stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$DevRoot = Join-Path $Root ".dev"
$Stage = Join-Path $DevRoot ("deploy-stage-" + $Stamp)
$Archive = Join-Path $DevRoot ("smartpipe-deploy-" + $Stamp + ".tar.gz")
$RemoteDir = "/tmp/smartpipe-deploy-" + $Stamp
$DevPrefix = ([IO.Path]::GetFullPath($DevRoot)).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
$StageFull = [IO.Path]::GetFullPath($Stage)
if (-not $StageFull.StartsWith($DevPrefix, [StringComparison]::OrdinalIgnoreCase)) {
  throw "Refusing staging directory outside .dev: $StageFull"
}

function Quote-Sh([string]$Value) {
  return "'" + $Value.Replace("'", "'\''") + "'"
}

try {
  New-Item -ItemType Directory -Path $Stage -Force | Out-Null

  if ($Mode -eq "binary") {
    if ($SkipBuild) {
      if (-not $BundleDir) { throw "Binary mode with -SkipBuild requires -BundleDir" }
      $BundleFull = [IO.Path]::GetFullPath($BundleDir)
      if (-not (Test-Path -LiteralPath (Join-Path $BundleFull "bin/admin-api"))) {
        throw "Linux bundle is incomplete: $BundleFull"
      }
      Copy-Item -LiteralPath $BundleFull -Destination (Join-Path $Stage "dist") -Recurse -Force
    } else {
      $buildArgs = @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $PSScriptRoot "build.ps1"),
        "-Version", $Version, "-OutputDir", (Join-Path $Stage "dist"), "-TargetOS", "linux",
        "-Arch", "amd64", "-SkipArchive"
      )
      if ($SkipWebBuild) { $buildArgs += "-SkipWeb" }
      & powershell.exe @buildArgs
      if ($LASTEXITCODE -ne 0) { throw "Linux build failed with exit code $LASTEXITCODE" }
    }
  }

  $tarArgs = @(
    "-czf", $Archive,
    "--exclude=.git", "--exclude=.syntropy", "--exclude=.dev", "--exclude=attachments",
    "--exclude=node_modules", "--exclude=web/node_modules", "--exclude=web/dist",
    "--exclude=backend/bin", "--exclude=backend/*.exe", "--exclude=backend/*.test",
    "--exclude=deploy/data", "--exclude=deploy/.env", "--exclude=__pycache__", "--exclude=*.pyc"
  )
  $tarArgs += @("-C", $Root)
  $sourceItems = @(
    "Dockerfile", ".dockerignore", "smartpipe.yaml", "README.md",
    "backend", "sdks", "web", "configs", "deploy", "scripts", "docs"
  )
  foreach ($item in $sourceItems) {
    if (Test-Path -LiteralPath (Join-Path $Root $item)) { $tarArgs += $item }
  }
  if ($Mode -eq "binary") {
    $tarArgs += @("-C", $Stage, "dist")
  }
  if ($EnvFile) {
    $envFull = (Resolve-Path -LiteralPath $EnvFile).Path
    Copy-Item -LiteralPath $envFull -Destination (Join-Path $Stage ".deploy.env") -Force
    $tarArgs += @("-C", $Stage, ".deploy.env")
  }
  & tar.exe @tarArgs
  if ($LASTEXITCODE -ne 0) { throw "tar failed with exit code $LASTEXITCODE" }

  $installArgs = @("--mode", $Mode, "--install-dir", $InstallDir)
  if ($EnvFile) { $installArgs += @("--env-file", ".deploy.env") }
  if ($Mode -eq "binary") { $installArgs += @("--bundle", "dist") }
  if ($SkipBuild -and $Mode -eq "docker") { $installArgs += "--skip-build" }
  $remoteCommand = "bash scripts/install.sh " + (($installArgs | ForEach-Object { Quote-Sh $_ }) -join " ")

  $pyArgs = @(
    (Join-Path $PSScriptRoot "remote-run.py"),
    "--host", $HostName, "--user", $User, "--port", "$Port",
    "--local", $Archive, "--dir", $RemoteDir, "--cmd", $remoteCommand, "--cleanup"
  )
  if ($PassFile) { $pyArgs += @("--pass-file", $PassFile) }
  if (-not $NoSudo) { $pyArgs += "--sudo" }

  Write-Output ("[deploy] " + $HostName + " mode=" + $Mode + " install_dir=" + $InstallDir)
  & python @pyArgs
  if ($LASTEXITCODE -ne 0) { throw "remote deployment failed with exit code $LASTEXITCODE" }
  Write-Output "DEPLOY OK"
} finally {
  $checkStage = [IO.Path]::GetFullPath($Stage)
  $checkArchive = [IO.Path]::GetFullPath($Archive)
  if ($checkStage.StartsWith($DevPrefix, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $checkStage)) {
    Remove-Item -LiteralPath $checkStage -Recurse -Force
  }
  if ($checkArchive.StartsWith($DevPrefix, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $checkArchive)) {
    Remove-Item -LiteralPath $checkArchive -Force
  }
}
