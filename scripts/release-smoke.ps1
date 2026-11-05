# M4 发版链路联调：假 GitHub + 假蓝信 + 本地沙箱仓库 + 真实 admin-api/数据库。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\release-smoke.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$api = "http://127.0.0.1:8092/admin"
$gh = "http://127.0.0.1:9099"
$bot = "http://127.0.0.1:9091"
$headers = @{ Authorization = "Bearer dev-admin-token" }
$repo = Join-Path $root ".dev\release-repo"
$repoURL = ([uri]$repo).AbsoluteUri
$projectName = "release-demo"
$versionOk = "v0.9.3"
$versionDry = "v0.9.2"
$versionSkip = "v0.9.1"
$versionFail = "v0.9.4"

function FixText($t) {
  if ($null -eq $t) { return "" }
  if ($t -match "[\u00c3\u00e3\u00e5\u00e6\u00a0-\u00bf]") {
    return [Text.Encoding]::UTF8.GetString([Text.Encoding]::GetEncoding(28591).GetBytes($t))
  }
  return $t
}
function J($obj) { $obj | ConvertTo-Json -Depth 5 -Compress }
function Api($method, $path, $body) {
  $params = @{ Method = $method; Uri = ($api + $path); Headers = $headers; ContentType = "application/json" }
  if ($null -ne $body) { $params.Body = (J $body) }
  Invoke-RestMethod @params
}
function Gh($method, $path, $body) {
  $params = @{ Method = $method; Uri = ($gh + $path); ContentType = "application/json" }
  if ($null -ne $body) { $params.Body = (J $body) }
  Invoke-RestMethod @params
}

# 每次从干净的假 GitHub 状态开始（清空 issue/tag/release）
Gh POST "/__reset" $null | Out-Null

$head = (git -C $repo rev-parse HEAD).Trim()
Gh POST "/__branch" @{ sha = $head } | Out-Null

$projects = (Api GET "/projects" $null).data
$project = $projects | Where-Object { $_.name -eq $projectName } | Select-Object -First 1
if (-not $project) {
  $project = (Api POST "/projects" @{
    name = $projectName; description = "M4 release smoke"
    github_repo_url = $repoURL; github_issue_repo = "example/release"
    github_default_branch = "master"; labels_prefix = "smartpipe"
    bluex_group_ids = "grp-release"; enabled = $true
  }).data
}
$creds = (Api GET "/credentials" $null).data
$cred = $creds | Where-Object { $_.project_id -eq $project.id -and $_.name -eq "fake-gh" } | Select-Object -First 1
if ($project.github_repo_url -ne $repoURL) {
  $project = (Api PATCH ("/projects/" + $project.id) @{ github_repo_url = $repoURL }).data
}
if (-not $cred) {
  Api POST "/credentials" @{
    project_id = $project.id; kind = "github_pat"; name = "fake-gh"; value = "fake-token"
    metadata = @{ api_base = $gh }
  } | Out-Null
}

# 1) 无候选 issue → skipped
$skip = (Api POST "/releases/runs" @{ project_id = $project.id; version = $versionSkip }).data.run
if ($skip.status -ne "skipped") { throw ("expected skipped, got " + $skip.status + ": " + $skip.error) }

# 2) 播种两个 ready issue
Gh POST "/__issues" @{ number = 101; title = "Login timeout"; labels = @("smartpipe:ready"); author = "alice" } | Out-Null
Gh POST "/__issues" @{ number = 102; title = "Export button unresponsive"; labels = @("smartpipe:ready"); author = "bob" } | Out-Null
$cands = (Api GET ("/releases/candidates?project_id=" + $project.id) $null).data
if (($cands.candidates | Measure-Object).Count -ne 2) { throw "expected 2 candidates, got $($cands.candidates.Count)" }

# 3) dry run 只构建，不发布
$dry = (Api POST "/releases/runs" @{ project_id = $project.id; version = $versionDry; dry_run = $true; artifact_globs = @("dist.txt") }).data.run
if ($dry.status -ne "built") { throw ("expected built, got " + $dry.status + ": " + $dry.error) }
$state = (Gh GET "/__state" $null)
if (($state.releases | Measure-Object).Count -ne 0 -or ($state.tags | Measure-Object).Count -ne 0) { throw "dry run must not publish" }

# 4) 完整发布
$rel = (Api POST "/releases/runs" @{ project_id = $project.id; version = $versionOk; notes = "Restart client after upgrade"; artifact_globs = @("dist.txt") }).data.run
if ($rel.status -ne "released") { throw ("release failed: " + $rel.status + " " + $rel.error) }
if ($rel.candidates.Count -ne 2) { throw ("candidates = " + $rel.candidates.Count) }
if ($rel.notify_status -ne "sent") { throw ("notify = " + $rel.notify_status + " " + $rel.notify_error) }
$state = (Gh GET "/__state" $null)
$tag = $state.tags | Where-Object { $_.tag -eq $versionOk }
if (-not $tag -or $tag.sha -ne $head) { throw "tag missing or wrong sha" }
if (($state.releases | Where-Object { $_.tag_name -eq $versionOk } | Measure-Object).Count -ne 1) { throw "github release missing" }
$relAssets = ($state.releases | Where-Object { $_.tag_name -eq $versionOk }).assets
if (-not $relAssets -or ($relAssets | Measure-Object).Count -lt 1) { throw "release assets missing" }
$relArtifact = $rel.artifacts | Select-Object -First 1
if (-not $relArtifact.download_url -or $relArtifact.download_url -notmatch "assets") { throw ("artifact download_url missing: " + (ConvertTo-Json $rel.artifacts -Compress)) }
Write-Output ("STEP assets -> " + $relAssets[0].name + " " + $relArtifact.download_url)
$i101 = $state.issues | Where-Object { $_.number -eq 101 }
$i101Labels = ($i101.labels | ForEach-Object { $_.name }) -join ","
if ($i101Labels -ne "smartpipe:released") { throw ("issue 101 labels = " + $i101Labels) }
$noteText = ($state.releases | Where-Object { $_.tag_name -eq $versionOk }).body
if ($noteText -notmatch "#101" -or $noteText -notmatch "Restart client") { throw "release note missing issue or notes" }
$replies = (Invoke-RestMethod -Uri ($bot + "/__replies")).replies
$lastReply = $replies | Select-Object -Last 1
if (-not $lastReply -or (FixText $lastReply.payload.text) -notmatch $versionOk) { throw "bluex notification missing" }
if ($lastReply.payload.targetId -ne "grp-release") { throw ("notify target = " + $lastReply.payload.targetId) }

# 5) 回滚演练：删 Release/tag，Issue 回到 ready
$rolled = (Api POST ("/releases/runs/" + $rel.id + "/rollback") $null).data
if ($rolled.status -ne "rolled_back") { throw ("rollback status = " + $rolled.status) }
$state = (Gh GET "/__state" $null)
if (($state.releases | Where-Object { $_.tag_name -eq $versionOk } | Measure-Object).Count -ne 0) { throw "release not deleted" }
if (($state.tags | Where-Object { $_.tag -eq $versionOk } | Measure-Object).Count -ne 0) { throw "tag not deleted" }
$i101 = $state.issues | Where-Object { $_.number -eq 101 }
if ((($i101.labels | ForEach-Object { $_.name }) -join ",") -ne "smartpipe:ready") { throw "issue not restored" }
$replies = (Invoke-RestMethod -Uri ($bot + "/__replies")).replies
if ((FixText ($replies | Select-Object -Last 1).payload.text) -notmatch "回滚") { throw "rollback notification missing" }

# 6) 构建失败不消费候选
$bad = (Api POST "/releases/runs" @{ project_id = $project.id; version = $versionFail; commit = "deadbeefdeadbeef"; artifact_globs = @("dist.txt") }).data.run
if ($bad.status -ne "failed") { throw ("expected failed, got " + $bad.status) }
$state = (Gh GET "/__state" $null)
$i102 = $state.issues | Where-Object { $_.number -eq 102 }
if ((($i102.labels | ForEach-Object { $_.name }) -join ",") -ne "smartpipe:ready") { throw "failed run consumed candidate" }
if (($state.releases | Where-Object { $_.tag_name -eq $versionFail } | Measure-Object).Count -ne 0) { throw "failed run published release" }

# 7) 记录入库
$runs = (Api GET "/releases/runs?limit=10" $null).data
$ids = $runs | ForEach-Object { $_.id }
if ($ids -notcontains $rel.id) { throw "release run not persisted" }

Write-Output ("RELEASE SMOKE OK project=" + $project.id + " repo=" + $repo)
Write-Output ("  skipped=" + $skip.id + " dry=" + $dry.id + " released=" + $rel.id + " rolled=" + $rolled.id + " failed=" + $bad.id)
Write-Output ("  tag=" + $versionOk + "@" + $head + " release_url=" + $rel.release_url)