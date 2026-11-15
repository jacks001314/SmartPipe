#!/bin/bash
# release-v2-smoke.sh：发版 v2 三输入入口的无人工冒烟。
#
# 入口只有三个输入，全部由这个脚本显式给出：
#   1. 项目          SMOKE_PROJECT_ID（或脚本自建 fixture 项目）
#   2. 升级服务器    SMOKE_UPDATE_SERVERS（默认取发布设置里启用的全部服务）
#   3. 构建机        SMOKE_EXECUTOR_IDS（默认由服务端按平台挑选在线构建机）
# 创建运行后不再有人工动作：服务端自动拉起发布 Agent，由它构建、打包、灌库、
# 推进渠道、发 GitHub；失败时 attention worker 自动续跑，直到收口或用尽重试。
#
# 模式（SMOKE_MODE）：
#   preview  只读：解析项目预览（版本/tag/commit/构建目标/升级服务/阻塞项）
#   publish  默认：三输入建运行，等 published，校验制品与升级服务发布记录
#   fail     用必失败的 fixture，等 Agent 用尽自动重试，校验停在 needs_attention
#
# 用法：
#   sudo bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_MODE=publish bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_PROJECT_ID=prj_xxx SMOKE_MODE=preview bash scripts/release-v2-smoke.sh
#   sudo SMOKE_MODE=fail bash scripts/release-v2-smoke.sh
#
# 环境变量：
#   SMARTPIPE_ADMIN_URL / SMARTPIPE_RELEASE_URL   接口地址
#   SMARTPIPE_ENV_FILE        默认 /etc/smartpipe/smartpipe.env（取 SMARTPIPE_ADMIN_TOKEN）
#   SMOKE_MODE                preview | publish | fail，默认 preview
#   SMOKE_PROJECT_ID          复用已有项目；留空则按 fixture 自建
#   SMOKE_FIXTURE_DIR         默认 /opt/smartpipe-v2-test/fixture
#   SMOKE_TARGET              自建项目时的构建目标，默认 linux-amd64
#   SMOKE_VERSION             自建项目时的版本，默认 0.0.<epoch>
#   SMOKE_EXECUTOR_IDS        逗号分隔的构建机 ID，留空则由服务端挑选
#   SMOKE_UPDATE_SERVERS      逗号分隔的升级服务器名，留空则取预览里启用的全部
#   SMOKE_CHANNEL             默认 test
#   SMOKE_TIMEOUT             等待运行的秒数，默认 3600
#   SMOKE_AGENT_TIMEOUT       等待 Agent 收口的秒数，默认 1800
#   SMOKE_EXPECT_RECOVERY     fail 模式期望的自动重试次数，默认 1
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:18092}
RELEASE=${SMARTPIPE_RELEASE_URL:-http://127.0.0.1:18099}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/etc/smartpipe/smartpipe.env}
MODE=${SMOKE_MODE:-preview}
PROJECT_ID=${SMOKE_PROJECT_ID:-}
FIXTURE_DIR=${SMOKE_FIXTURE_DIR:-/opt/smartpipe-v2-test/fixture}
TARGET=${SMOKE_TARGET:-linux-amd64}
CHANNEL=${SMOKE_CHANNEL:-test}
TIMEOUT=${SMOKE_TIMEOUT:-3600}
AGENT_TIMEOUT=${SMOKE_AGENT_TIMEOUT:-1800}
EXPECT_RECOVERY=${SMOKE_EXPECT_RECOVERY:-1}

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
json_field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }
api() { curl -fsS -H "$AUTH" -H "$CT" "$@"; }

case "$MODE" in preview|publish|fail) ;; *) fail "SMOKE_MODE 只能是 preview/publish/fail（当前 $MODE）" ;; esac

if [[ -z "${SMARTPIPE_ADMIN_TOKEN:-}" ]]; then
  [[ -r "$ENV_FILE" ]] || fail "缺少 SMARTPIPE_ADMIN_TOKEN，且无法读取 $ENV_FILE"
  # shellcheck disable=SC1090
  set -a; . "$ENV_FILE"; set +a
fi
[[ -n "${SMARTPIPE_ADMIN_TOKEN:-}" ]] || fail "$ENV_FILE 里没有 SMARTPIPE_ADMIN_TOKEN"
AUTH="Authorization: Bearer ${SMARTPIPE_ADMIN_TOKEN}"
CT="Content-Type: application/json"

admin_version=$(curl -fsS "$ADMIN/healthz" | json_field 'd.get("version","")')
[[ -n "$admin_version" ]] || fail "admin-api /healthz 不可用"
pass "admin-api 健康：$admin_version"
[[ "$(curl -fsS "$RELEASE/healthz" | json_field 'd.get("status","")')" == "ok" ]] || fail "release-service 健康检查失败"
pass "release-service 健康"

STAMP=$(date +%s)
VERSION=${SMOKE_VERSION:-0.0.$((STAMP % 100000))}

# prepare_project 在 fixture 上造一个自带版本文件的本地仓库，并注册成项目。
# 版本、tag、commit 都由服务端从项目和仓库派生，不再从请求体传入。
prepare_project() {
  local work repo recipe_glob project cfg
  work=${SMOKE_WORK_DIR:-/tmp/release-v2-smoke-$STAMP}
  repo=$work/repo
  [[ -d "$FIXTURE_DIR/.git" ]] || fail "$FIXTURE_DIR 不是 git 仓库"
  mkdir -p "$repo"
  printf '{"version":"%s"}\n' "$VERSION" > "$repo/version.json"
  printf 'smoke manifest\n' > "$repo/README.md"
  if [[ "$MODE" == "fail" ]]; then
    recipe_glob='echo boom: intentional build failure >&2; exit 7'
  else
    recipe_glob='mkdir -p dist && date -u > dist/app.txt && tar -czf dist/app.tar.gz dist/app.txt'
  fi
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.name=smoke -c user.email=smoke@example.invalid add -A
  git -C "$repo" -c user.name=smoke -c user.email=smoke@example.invalid commit -qm "smoke $VERSION"
  git -C "$repo" -c user.name=smoke -c user.email=smoke@example.invalid tag "v$VERSION"
  pass "本地源码仓库：$repo（v$VERSION）"

  project=$(api -X POST -d "{\"name\":\"smoke-$STAMP\",\"github_repo_url\":\"file://$repo\",\"github_default_branch\":\"main\",\"local_repo_path\":\"$repo\",\"enabled\":true}" "$ADMIN/admin/projects")
  PROJECT_ID=$(echo "$project" | json_field 'd["data"]["id"]')
  [[ "$PROJECT_ID" == prj_* ]] || fail "创建项目失败：$project"
  pass "项目：$PROJECT_ID"

  cfg=$(python3 - "$TARGET" "$VERSION" "$recipe_glob" <<'PY'
import json, sys
target, version, build = sys.argv[1], sys.argv[2], sys.argv[3]
recipe = {
    "build": build,
    "sign": "sh -c 'date -u > dist/app.sig && printf SIGNED >> dist/app.sig'",
    "verify": "test -s dist/app.tar.gz",
    "artifacts": [{
        "role": "app-bundle",
        "glob": "dist/app.tar.gz",
        "content_type": "application/gzip",
        "destinations": ["update:full"],
    }],
}
print(json.dumps({
    "builds": {target: recipe},
    "version_file": "version.json",
    "channel_order": ["test", "stable"],
    "publishing": {"update_service": {"product": "fixture", "channel": "test",
                                      "productSlots": {"app": {"artifactRole": "app-bundle"}}}},
}))
PY
)
  api -X PUT -d "$cfg" "$ADMIN/admin/releases/v2/projects/$PROJECT_ID/release-config" >/dev/null || fail "写入项目发布配置失败"
  pass "项目发布配置已写入（$TARGET，version_file=version.json）"
}

if [[ -z "$PROJECT_ID" ]]; then
  prepare_project
fi

# preview 是三输入入口的只读校验：版本、tag、commit、构建目标、升级服务。
PREVIEW=$(api "$ADMIN/admin/releases/v2/projects/$PROJECT_ID/release-preview") || fail "读取发布预览失败（项目 $PROJECT_ID）"
PREVIEW_FILE=$(mktemp)
printf '%s' "$PREVIEW" > "$PREVIEW_FILE"
python3 - "$PREVIEW_FILE" <<'PY' || { rm -f "$PREVIEW_FILE"; fail "发布预览不合格"; }
import json, sys
d = json.load(open(sys.argv[1]))["data"]
print("PASS: 版本 %s tag %s commit %s" % (d.get("version"), d.get("tag"), (d.get("commit") or "")[:12]))
for b in d["builds"]:
    print("  build %s %s/%s executor=%s ready=%s" % (b["target_id"], b["os"], b["arch"], b.get("executor_id") or "-", b.get("ready")))
for s in d["update_servers"]:
    print("  server %s %s enabled=%s" % (s["id"], s["url"], s["enabled"]))
if d.get("blockers"):
    print("FAIL: 阻塞项 %s" % json.dumps(d["blockers"], ensure_ascii=False), file=sys.stderr)
    sys.exit(1)
if not d["builds"]:
    print("FAIL: 没有构建目标", file=sys.stderr)
    sys.exit(1)
if not any(s["enabled"] for s in d["update_servers"]):
    print("FAIL: 发布设置里没有启用的升级服务器", file=sys.stderr)
    sys.exit(1)
PY
rm -f "$PREVIEW_FILE"
pass "预览无阻塞项"

if [[ "$MODE" == "preview" ]]; then
  echo "SMOKE_OK project=$PROJECT_ID mode=preview"
  exit 0
fi

read_list() {
  local raw=$1
  python3 -c "import sys,json;print(json.dumps([x.strip() for x in sys.argv[1].split(',') if x.strip()]))" "$raw"
}

if [[ -n "${SMOKE_UPDATE_SERVERS:-}" ]]; then
  SERVERS=$(read_list "$SMOKE_UPDATE_SERVERS")
else
  SERVERS=$(echo "$PREVIEW" | json_field 'json.dumps([s["id"] for s in d["data"]["update_servers"] if s["enabled"]])')
fi
[[ "$SERVERS" != "[]" ]] || fail "没有可用的升级服务器"
if [[ -n "${SMOKE_EXECUTOR_IDS:-}" ]]; then
  EXECUTORS=$(read_list "$SMOKE_EXECUTOR_IDS")
else
  EXECUTORS='[]'
fi
pass "升级服务器：$SERVERS；构建机：$EXECUTORS"

BODY=$(python3 -c 'import json,sys;print(json.dumps({"project_id":sys.argv[1],"update_server_ids":json.loads(sys.argv[2]),"executor_ids":json.loads(sys.argv[3]),"idempotency_key":"smoke-"+sys.argv[4]}))' "$PROJECT_ID" "$SERVERS" "$EXECUTORS" "$STAMP")
RUN=""
RUN=$(api -X POST -d "$BODY" "$ADMIN/admin/releases/v2/runs") || fail "创建运行失败：$RUN"
RUN_ID=$(echo "$RUN" | json_field 'd["data"]["id"]')
[[ "$RUN_ID" == rrun_* ]] || fail "创建运行失败：$RUN"
pass "运行已创建（三输入，无人工审批）：$RUN_ID"

run_status() {
  api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | json_field 'd["data"].get("run",d["data"])["status"]'
}

agent_running() {
  api "$ADMIN/admin/releases/v2/runs/$RUN_ID/agent" | json_field 'str(d["data"].get("running")).lower()'
}

show_events_tail() {
  api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
for ev in (d.get("events") or [])[-12:]:
    print("  %s %s %s" % (ev.get("actor"), ev.get("kind"), (ev.get("summary") or "")[:160]))
' 2>/dev/null || true
}

DEADLINE=$((SECONDS + TIMEOUT))
STATUS=""
while (( SECONDS < DEADLINE )); do
  STATUS=$(run_status)
  case "$MODE:$STATUS" in
    publish:published|fail:needs_attention) break ;;
    *:failed|*:cancelled|*:blocked) break ;;
  esac
  sleep 5
done
[[ -n "$STATUS" ]] || fail "读取运行状态失败"
pass "运行状态：$STATUS"

if [[ "$MODE" == "publish" ]]; then
  if [[ "$STATUS" != "published" ]]; then
    show_events_tail
    fail "等待 $TIMEOUT 秒仍未 published（当前 $STATUS）"
  fi
  DETAIL=$(api "$ADMIN/admin/releases/v2/runs/$RUN_ID")
  DETAIL_FILE=$(mktemp)
  printf '%s' "$DETAIL" > "$DETAIL_FILE"
  python3 - "$DETAIL_FILE" <<'PY' || { rm -f "$DETAIL_FILE"; fail "发布结果校验失败"; }
import json, sys
d = json.load(open(sys.argv[1]))["data"]
run = d.get("run", d)
targets = d.get("targets") or []
arts = d.get("artifacts") or []
pubs = d.get("publications") or []
bad = [t.get("target_id") for t in targets if t.get("status") != "ready"]
if bad:
    print("FAIL: 目标未 ready: %s" % bad, file=sys.stderr); sys.exit(1)
missing = [a.get("name") for a in arts if not str(a.get("sha256") or "").startswith("sha256:")]
if not arts or missing:
    print("FAIL: 制品缺摘要: %s" % (missing or "无制品"), file=sys.stderr); sys.exit(1)
state = next((p.get("state") for p in pubs if p.get("provider") == "update_service"), "")
if state != "promoted":
    print("FAIL: update_service 发布记录 state=%s" % state, file=sys.stderr); sys.exit(1)
print("PASS: 发布收口 %s，目标 %d 个 ready，制品 %d 个，update_service=promoted" % (run.get("id"), len(targets), len(arts)))
for a in arts:
    print("  - %s %s %s" % (a.get("role"), a.get("sha256"), a.get("destinations")))
for p in pubs:
    print("  pub %s %s %s" % (p.get("provider"), p.get("state"), p.get("external_id") or "-"))
PY
  rm -f "$DETAIL_FILE"
  echo "SMOKE_OK run=$RUN_ID project=$PROJECT_ID version=$VERSION mode=publish"
  exit 0
fi

# fail 模式：Agent 自动重试有上限，跑满后必须停在 needs_attention，且不登记发布。
DEADLINE=$((SECONDS + AGENT_TIMEOUT))
ATTEMPTS=0
while (( SECONDS < DEADLINE )); do
  DETAIL=$(api "$ADMIN/admin/releases/v2/runs/$RUN_ID")
  ATTEMPTS=$(echo "$DETAIL" | json_field '(d["data"].get("run") or d["data"]).get("recovery_attempts") or 0')
  RUNNING=$(agent_running)
  if [[ "$RUNNING" == "false" ]] && (( ATTEMPTS >= EXPECT_RECOVERY )); then
    break
  fi
  sleep 5
done
FAIL_FILE=$(mktemp)
printf '%s' "$DETAIL" > "$FAIL_FILE"
python3 - "$FAIL_FILE" "$EXPECT_RECOVERY" <<'PY' || { rm -f "$FAIL_FILE"; fail "失败路径校验失败"; }
import json, sys
expect = int(sys.argv[2])
d = json.load(open(sys.argv[1]))["data"]
run = d.get("run", d)
status = run.get("status")
attempts = run.get("recovery_attempts") or 0
pubs = d.get("publications") or []
if status != "needs_attention":
    print("FAIL: 失败后状态应为 needs_attention（当前 %s）" % status, file=sys.stderr); sys.exit(1)
if attempts < expect:
    print("FAIL: 自动重试次数 %d 少于期望 %d" % (attempts, expect), file=sys.stderr); sys.exit(1)
if pubs:
    print("FAIL: 失败运行不应登记发布记录（%d 条）" % len(pubs), file=sys.stderr); sys.exit(1)
print("PASS: 自动重试 %d 次后停在 needs_attention，无发布记录" % attempts)
print("  错误：%s / %s" % (run.get("error_code") or "-", (run.get("error_message") or "")[:160]))
PY
rm -f "$FAIL_FILE"
echo "SMOKE_OK run=$RUN_ID project=$PROJECT_ID version=$VERSION mode=fail attempts=$ATTEMPTS"
