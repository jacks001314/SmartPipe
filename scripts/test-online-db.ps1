param(
  [int]$Port = 55439
)
$ErrorActionPreference = 'Stop'
# Dedicated ephemeral DB only. Never imports local credentials or existing DSNs.
Get-Command docker -ErrorAction Stop | Out-Null
$container = 'smartpipe-test-' + [Guid]::NewGuid().ToString('N').Substring(0,12)
$previousDsn = $env:SMARTPIPE_TEST_DSN
$previousSumDb = $env:GOSUMDB
$previousWorker = $env:SMARTPIPE_WORKER_JS
$started = $false
Push-Location (Join-Path $PSScriptRoot '../test-runner')
try {
  & npm run build
  if ($LASTEXITCODE -ne 0) { throw 'Build TypeScript Worker before database tests' }
} finally { Pop-Location }
$env:SMARTPIPE_WORKER_JS = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../test-runner/dist/src/worker.js'))
try {
  & docker run --detach --rm --name $container -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=smartpipe_online_test -p "127.0.0.1:${Port}:5432" postgres:17
  if ($LASTEXITCODE -ne 0) { throw 'Cannot start dedicated PostgreSQL container' }
  $started = $true
  $ready = $false
  for ($attempt=0; $attempt -lt 30; $attempt++) {
    & docker exec $container pg_isready -U postgres -d smartpipe_online_test *> $null
    if ($LASTEXITCODE -eq 0) { $ready=$true; break }
    Start-Sleep -Seconds 1
  }
  if (-not $ready) { throw 'PostgreSQL did not become ready' }
  $env:SMARTPIPE_TEST_DSN = "postgres://postgres@127.0.0.1:${Port}/smartpipe_online_test?sslmode=disable"
  $env:GOSUMDB = 'sum.golang.org'
  Push-Location (Join-Path $PSScriptRoot '../backend')
  try {
    & go test -p=1 ./internal/testdb ./internal/patch ./internal/testlab ./internal/store ./internal/adminapi ./internal/releasemcp -count=1 -v
    if ($LASTEXITCODE -ne 0) { throw 'Online database integration tests failed' }
  } finally { Pop-Location }
} finally {
  $env:SMARTPIPE_TEST_DSN = $previousDsn
  $env:GOSUMDB = $previousSumDb
  $env:SMARTPIPE_WORKER_JS = $previousWorker
  if ($started) {
    & docker stop $container | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Warning "Test container cleanup failed: $container" }
  }
}
