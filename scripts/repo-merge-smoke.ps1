# repo.commit / repo.push 全链路联调：真实 admin-api + 假 GitHub + 本地裸库。
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File scripts\repo-merge-smoke.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$api = "http://127.0.0.1:8092/admin"
$mcp = "http://127.0.0.1:8097/mcp"
$adminHeaders = @{ Authorization = "Bearer dev-admin-token" }

function PostJson($url, $payload, $headers) {
  $tmp = Join-Path $env:TEMP ("sp-merge-" + [guid]::NewGuid().ToString("N") + ".json")
  [IO.File]::WriteAllText($tmp, ($payload | ConvertTo-Json -Depth 4 -Compress), [Text.UTF8Encoding]::new($false))
  $argv = @("-s", "-m", "300", "-o", "$tmp.out", "-w", "%{http_code}", "-X", "POST", "-H", "Content-Type: application/json")
  foreach ($k in $headers.Keys) { $argv += @("-H", ($k + ": " + $headers[$k])) }
  $argv += @("--data-binary", "@$tmp", $url)
  $status = & curl.exe @argv
  $text = [IO.File]::ReadAllText("$tmp.out", [Text.UTF8Encoding]::new($false))
  return @{ Status = [int]$status; Text = $text; Body = ($text | ConvertFrom-Json) }
}
function GetJson($url, $headers) {
  $tmp = Join-Path $env:TEMP ("sp-merge-g-" + [guid]::NewGuid().ToString("N") + ".json")
  $argv = @("-s", "-m", "60", "-o", $tmp, "-w", "%{http_code}")
  foreach ($k in $headers.Keys) { $argv += @("-H", ($k + ": " + $headers[$k])) }
  $argv += $url
  $status = & curl.exe @argv
  $text = [IO.File]::ReadAllText($tmp, [Text.UTF8Encoding]::new($false))
  return @{ Status = [int]$status; Text = $text; Body = ($text | ConvertFrom-Json) }
}
function Tool($token, $name, $arguments) {
  $r = PostJson $mcp @{ jsonrpc = "2.0"; id = (Get-Random -Maximum 100000); method = "tools/call"; params = @{ name = $name; arguments = $arguments } } @{ Authorization = "Bearer $token" }
  if ($r.Status -ne 200) { throw ("tools/call HTTP " + $r.Status + ": " + $r.Text) }
  $res = $r.Body.result
  if ($res.isError -eq $true) { throw ($name + " failed: " + $res.content[0].text) }
  return ($res.content[0].text | ConvertFrom-Json)
}

$remoteBare = (Join-Path $root ".dev\remote.git")
$remoteURL = "file:///" + ($remoteBare -replace '\\', '/')
$srcRepo = (Join-Path $root ".dev\sandbox-repo")

# 1) 项目：issue 走假 GitHub，代码远端指向本地裸库
$projBody = @{
  "name" = ("merge-demo-" + (Get-Date -Format "HHmmss"))
  "github_repo_url" = $remoteURL
  "github_issue_repo" = "example/release"
  "github_default_branch" = "master"
  "labels_prefix" = "smartpipe"
  "bluex_group_ids" = "grp-release"
}
$proj = PostJson ($api + "/projects") $projBody $adminHeaders
if ($proj.Status -ne 201) { throw ("create project failed: " + $proj.Status + " " + $proj.Text) }
$projectID = $proj.Body.data.id
Write-Output ("project=" + $projectID + " remote=" + $remoteURL)

# 2) 干净工作副本
$work = (Join-Path $root ".dev\merge-work")
if (Test-Path $work) { Remove-Item -LiteralPath $work -Recurse -Force }
& git clone --quiet $srcRepo $work
if ($LASTEXITCODE -ne 0) { throw "clone failed" }
$base = (& git -C $work rev-parse HEAD).Trim()
Write-Output ("work=" + $work + " base=" + $base)

# 3) lead 令牌
$tok = PostJson ($api + "/agent-tokens") @{ project_id = $projectID; role = "lead"; name = "merge-lead" } $adminHeaders
if ($tok.Status -ne 201) { throw ("token failed: " + $tok.Text) }
$leadToken = $tok.Body.data.token
$tokenID = $tok.Body.data.record.id

# 4) repo.commit
$patch = [string][IO.File]::ReadAllText((Join-Path $root ".dev\patch-multiply.diff"), [Text.UTF8Encoding]::new($false))
$commit = Tool $leadToken "repo.commit" @{
  repo = $work; base_commit = $base; patch = $patch; issue_ref = "example/release#101"
  message = "fix: add Multiply (smoke)"; target_branch = ""
}
Write-Output ("commit ok=" + $commit.ok + " branch=" + $commit.branch + " sha=" + $commit.commit)
if ($commit.ok -ne $true) { throw ("commit rejected: " + $commit.error) }

# 5) repo.push
$push = Tool $leadToken "repo.push" @{ repo = $work; branch = $commit.branch; remote = "origin"; issue_ref = "example/release#101" }
Write-Output ("push ok=" + $push.ok + " branch=" + $push.branch + " after=" + $push.after)
if ($push.ok -ne $true) { throw ("push rejected: " + $push.error) }

# 6) 远端确有该分支，且两端 commit 一致
$refName = "refs/heads/" + $commit.branch
$remoteRef = (& git -C $remoteBare rev-parse $refName 2>$null)
if ($LASTEXITCODE -ne 0 -or -not $remoteRef) { throw ("branch not on remote: " + $commit.branch) }
if ($remoteRef.Trim() -ne $commit.commit) { throw ("remote head " + $remoteRef + " != commit " + $commit.commit) }

# 7) 运维视图能看到两条记录
$ops = GetJson ($api + "/repo-ops?limit=5") $adminHeaders
$kinds = @($ops.Body.data | ForEach-Object { $_.kind })
if ($kinds -notcontains "commit" -or $kinds -notcontains "push") { throw ("repo-ops = " + ($kinds -join ",")) }

# 8) dev 令牌看不到这两个工具
$devTok = PostJson ($api + "/agent-tokens") @{ project_id = $projectID; role = "dev"; name = "merge-dev" } $adminHeaders
$devToken = $devTok.Body.data.token
$devList = PostJson $mcp @{ jsonrpc = "2.0"; id = 9; method = "tools/list" } @{ Authorization = "Bearer $devToken" }
$devTools = @($devList.Body.result.tools | ForEach-Object { $_.name })
if ($devTools -contains "repo.commit" -or $devTools -contains "repo.push") { throw "dev must not see repo tools" }

# 9) 清理临时令牌
$null = PostJson ($api + "/agent-tokens/" + $tokenID + "/revoke") @{} $adminHeaders
$null = PostJson ($api + "/agent-tokens/" + $devTok.Body.data.record.id + "/revoke") @{} $adminHeaders

Write-Output ("REPO MERGE SMOKE OK project=" + $projectID + " commit=" + $commit.commit + " remote_head=" + $remoteRef.Trim())
