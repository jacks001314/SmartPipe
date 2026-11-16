#!/bin/bash
# release-v2-version-gate-drill.sh：验证「同版本换字节」被拦在 release_import。
#
# 场景（替身升级服务 / 替身 GitHub，不碰线上渠道）：
#   1. 合成项目版本 0.9.80001，第一次发布到 published。
#   2. 同一版本再发一次：构建产物字节必然不同（构建脚本写入纳秒时间戳），
#      Agent 调 release_import 时必须拿到 version_already_published，
#      而不是把线上同版本的槽位静默改写。
#   3. 断言：第二次运行没有 published、事件里出现 version_conflicts、
#      替身上该版本的槽位摘要与第一次完全一致。
#
# 用法：
#   sudo bash scripts/release-v2-version-gate-drill.sh
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:8092}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/opt/smartpipe/deploy/.env}
NETWORK=${DRILL_NETWORK:-smartpipe_default}
UPDATE_PORT=${DRILL_UPDATE_PORT:-18400}
GH_PORT=${DRILL_GITHUB_PORT:-9399}
EXECUTOR=${DRILL_EXECUTOR:-mac-01}
TIMEOUT=${DRILL_TIMEOUT:-600}
VERSION=${DRILL_VERSION:-0.9.80001}
STAMP=$(date +%s)
WORK_HOST=${DRILL_WORK_DIR:-/opt/smartpipe/deploy/data/drill/version-gate-$STAMP}
case "$WORK_HOST" in
  /opt/smartpipe/deploy/data/*) WORK="/data/${WORK_HOST#/opt/smartpipe/deploy/data/}" ;;
  *) echo "FAIL: DRILL_WORK_DIR 必须在 /opt/smartpipe/deploy/data 下（容器挂载点）" >&2; exit 1 ;;
esac
APP_UID=${DRILL_APP_UID:-10001}
APP_GID=${DRILL_APP_GID:-10001}
CRED=drill-token
GH_CRED=ghp_fake_rehearsal
SERVICE=vg-primary
FAKE_DIR=${DRILL_FAKE_DIR:-/home/jacks/fake-update}
TARGET=darwin-amd64

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
jf() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }
api() { curl -fsS -H "$AUTH" -H "$CT" "$@"; }

[[ $EUID -eq 0 ]] || fail "需要 root：要改发布设置、起替身服务"
if [[ -z "${SMARTPIPE_ADMIN_TOKEN:-}" ]]; then
  [[ -r "$ENV_FILE" ]] || fail "缺少 SMARTPIPE_ADMIN_TOKEN 且无法读取 $ENV_FILE"
  set -a; . "$ENV_FILE"; set +a
fi
[[ -n "${SMARTPIPE_ADMIN_TOKEN:-}" ]] || fail "$ENV_FILE 里没有 SMARTPIPE_ADMIN_TOKEN"
AUTH="Authorization: Bearer ${SMARTPIPE_ADMIN_TOKEN}"
CT="Content-Type: application/json"

GW=$(docker network inspect "$NETWORK" | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["IPAM"]["Config"][0]["Gateway"])') || fail "取不到 $NETWORK 网关"
UPDATE_URL="http://$GW:$UPDATE_PORT"
GH_URL="http://$GW:$GH_PORT"

mkdir -p "$FAKE_DIR" "$WORK_HOST"
chmod 700 "$WORK_HOST"
chown "$APP_UID:$APP_GID" "$WORK_HOST"
SETTINGS_BACKUP="$WORK_HOST/settings.backup.json"
STARTED=()
ORPHAN_CREDS=()

stop_listener() {
  local port=$1 pids
  pids=$(lsof -ti "tcp:$port" 2>/dev/null || true)
  if [[ -n "$pids" ]]; then
    kill $pids 2>/dev/null || true
    sleep 1
  fi
}

start_update() {
  stop_listener "$UPDATE_PORT"
  ( cd /opt/smartpipe && nohup python3 scripts/fake-update-service.py --host 0.0.0.0 --port "$UPDATE_PORT" > "$FAKE_DIR/vg-update.log" 2>&1 & echo $! > "$FAKE_DIR/vg-update.pid" )
  STARTED+=("$FAKE_DIR/vg-update.pid")
  local i
  for i in $(seq 1 40); do
    curl -fsS --max-time 2 "http://127.0.0.1:$UPDATE_PORT/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  fail "替身升级服务 $UPDATE_PORT 没起来（$FAKE_DIR/vg-update.log）"
}

start_github() {
  stop_listener "$GH_PORT"
  ( cd /opt/smartpipe && FAKE_GITHUB_HOST=0.0.0.0 nohup node scripts/fake-github.mjs "$GH_PORT" > "$FAKE_DIR/vg-github.log" 2>&1 & echo $! > "$FAKE_DIR/vg-github.pid" )
  STARTED+=("$FAKE_DIR/vg-github.pid")
  local i
  for i in $(seq 1 40); do
    curl -fsS --max-time 2 "http://127.0.0.1:$GH_PORT/__state" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  fail "替身 GitHub 没起来（$FAKE_DIR/vg-github.log）"
}

stop_started() {
  local pidfile
  for pidfile in "${STARTED[@]:-}"; do
    [[ -f "$pidfile" ]] || continue
    kill "$(cat "$pidfile")" 2>/dev/null || true
    rm -f "$pidfile"
  done
  stop_listener "$UPDATE_PORT"
  stop_listener "$GH_PORT"
}

restore_settings() {
  [[ -s "$SETTINGS_BACKUP" ]] || return 0
  local i
  for i in $(seq 1 30); do
    if curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$SETTINGS_BACKUP" "$ADMIN/admin/releases/v2/settings" >/dev/null 2>&1; then
      echo "PASS: 发布设置已恢复"
      return 0
    fi
    sleep 2
  done
  echo "WARN: 恢复发布设置失败，备份在 $SETTINGS_BACKUP" >&2
}

cleanup_credentials() {
  local name id
  for name in "${ORPHAN_CREDS[@]:-}"; do
    [[ -n "$name" ]] || continue
    id=$(api "$ADMIN/admin/credentials" 2>/dev/null \
      | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(next((c["id"] for c in d if c.get("name") == sys.argv[1]), ""))' "$name")
    [[ -n "$id" ]] || continue
    api -X DELETE "$ADMIN/admin/credentials/$id" >/dev/null 2>&1 \
      && echo "PASS: 已清理演练凭据 $name" \
      || echo "WARN: 清理演练凭据 $name 失败" >&2
  done
}
cleanup() { restore_settings; stop_started; cleanup_credentials; }
trap cleanup EXIT

curl -fsS "$ADMIN/healthz" >/dev/null || fail "admin-api 不健康"
api "$ADMIN/admin/releases/v2/settings" | python3 -c 'import json,sys; json.dump(json.load(sys.stdin)["data"], sys.stdout)' > "$SETTINGS_BACKUP"
chmod 600 "$SETTINGS_BACKUP"
python3 - "$SETTINGS_BACKUP" <<'PY' || fail "发布设置备份为空，拒绝在裸设置上演练"
import json, sys
sys.exit(0 if json.load(open(sys.argv[1])).get("update_services") else 1)
PY
pass "已备份发布设置"

start_update && start_github && pass "替身就绪：update=$UPDATE_URL github=$GH_URL"

DRILL_SETTINGS="$WORK_HOST/settings.drill.json"
python3 - "$SETTINGS_BACKUP" "$UPDATE_URL" "$GH_URL" "$CRED" "$GH_CRED" > "$DRILL_SETTINGS" <<'PY'
import json, sys
src, update, gh, cred, gh_cred = sys.argv[1:6]
d = json.load(open(src))
d["update_services"] = [{"name": "vg-primary", "url": update, "credential": cred, "enabled": True}]
d["update_url"] = update
d["update_credential"] = cred
d["github_api_base"] = gh
d["github_credential"] = gh_cred
print(json.dumps(d))
PY
curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$DRILL_SETTINGS" "$ADMIN/admin/releases/v2/settings" >/dev/null || fail "切换发布设置失败"
pass "发布设置已切到替身（退出自动恢复）"

ensure_credential() {
  local kind=$1 name=$2 present
  present=$(api "$ADMIN/admin/credentials" 2>/dev/null | jf "any(c.get('name')==\"$name\" for c in d['data'])")
  if [[ "$present" != "True" ]]; then
    api -X POST -d "{\"kind\":\"$kind\",\"name\":\"$name\",\"value\":\"$name\"}" "$ADMIN/admin/credentials" >/dev/null || fail "登记凭据 $name 失败"
    ORPHAN_CREDS+=("$name")
  fi
}
ensure_credential github_pat "$GH_CRED"
ensure_credential release_update "$CRED"

REPO="$WORK/repo"
REPO_HOST="$WORK_HOST/repo"
mkdir -p "$REPO_HOST"
git -C "$REPO_HOST" init -q -b main
printf '{"version":"%s"}\n' "$VERSION" > "$REPO_HOST/version.json"
RECIPE_BUILD="mkdir -p dist && date -u +%s%N > dist/app.txt && tar -czf dist/app.tar.gz dist/app.txt"
git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST" add -A
git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST" commit -qm "drill version gate"
git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST" tag -f "v$VERSION" >/dev/null
chown -R "$APP_UID:$APP_GID" "$REPO_HOST"

PROJECT=$(api -X POST -d "{\"name\":\"vg-$STAMP\",\"github_repo_url\":\"file://$REPO\",\"github_default_branch\":\"main\",\"local_repo_path\":\"$REPO\",\"enabled\":true}" "$ADMIN/admin/projects")
PROJECT_ID=$(echo "$PROJECT" | jf 'd["data"]["id"]')
[[ "$PROJECT_ID" == prj_* ]] || fail "创建项目失败：$PROJECT"
pass "项目：$PROJECT_ID"

CFG="$WORK_HOST/config.json"
python3 - "$TARGET" "$RECIPE_BUILD" > "$CFG" <<'PY'
import json, sys
target, build = sys.argv[1], sys.argv[2]
print(json.dumps({
    "builds": {target: {
        "build": build,
        "sign": "sh -c 'date -u > dist/app.sig && printf SIGNED >> dist/app.sig'",
        "verify": "test -s dist/app.tar.gz",
        "artifacts": [{
            "role": "app-bundle", "glob": "dist/app.tar.gz",
            "content_type": "application/gzip", "destinations": ["github", "update:full"],
        }],
    }},
    "version_file": "version.json",
    "channel_order": ["beta", "stable"],
    "publishing": {
        "github": {"owner": "smoke", "repo": "fixture"},
        "update_service": {"product": "fixture", "channel": "beta",
                           "productSlots": {"app": {"artifactRole": "app-bundle"}}},
    },
}))
PY
curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$CFG" "$ADMIN/admin/releases/v2/projects/$PROJECT_ID/release-config" >/dev/null || fail "写入项目发布配置失败"

create_run() {
  local tag=$1
  api -X POST -d "{\"project_id\":\"$PROJECT_ID\",\"update_server_ids\":[\"$SERVICE\"],\"executor_ids\":[\"$EXECUTOR\"],\"idempotency_key\":\"$tag-$STAMP\"}" \
    "$ADMIN/admin/releases/v2/runs"
}
run_status() { api "$ADMIN/admin/releases/v2/runs/$1" 2>/dev/null | jf 'd["data"].get("run",d["data"])["status"]'; }
show_tail() {
  api "$ADMIN/admin/releases/v2/runs/$1" 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
for ev in (d.get("events") or [])[-10:]:
    print("  %s %s %s" % (ev.get("actor"), ev.get("kind"), (ev.get("summary") or "")[:140]))
' 2>/dev/null || true
}

RUN1=$(create_run first)
RUN1_ID=$(echo "$RUN1" | jf 'd["data"]["id"]')
[[ "$RUN1_ID" == rrun_* ]] || fail "第一次创建运行失败：$RUN1"
pass "第一次运行：$RUN1_ID（version=$(echo "$RUN1" | jf 'd["data"]["version"]')）"

deadline=$((SECONDS + TIMEOUT))
while (( SECONDS < deadline )); do
  status=$(run_status "$RUN1_ID" || true)
  [[ "$status" == published ]] && break
  case "$status" in
    failed|blocked|cancelled) show_tail "$RUN1_ID"; fail "第一次运行停在 $status" ;;
  esac
  sleep 3
done
[[ "${status:-}" == published ]] || { show_tail "$RUN1_ID"; fail "第一次运行没有在 ${TIMEOUT}s 内 published（当前 ${status:-unknown}）"; }
pass "第一次运行 published"

digest_before=$(curl -fsS "$UPDATE_URL/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for rel in (d.get("releases") or {}).values():
    if str(rel.get("version")) != sys.argv[1]:
        continue
    for a in rel.get("artifacts") or []:
        if a.get("slot") == "app":
            print(a.get("sha256")); raise SystemExit(0)
raise SystemExit(1)
' "$VERSION") || fail "替身上找不到 $VERSION 的 app 槽位摘要"
pass "第一次发布的 app 槽位摘要：${digest_before:0:12}…"

RUN2=$(create_run second)
RUN2_ID=$(echo "$RUN2" | jf 'd["data"]["id"]')
[[ "$RUN2_ID" == rrun_* ]] || fail "第二次创建运行失败：$RUN2"
pass "第二次运行（同版本）：$RUN2_ID"

conflict=0
deadline=$((SECONDS + TIMEOUT))
while (( SECONDS < deadline )); do
  out=$(api "$ADMIN/admin/releases/v2/runs/$RUN2_ID" 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
hits=[e for e in (d.get("events") or []) if (e.get("detail") or {}).get("version_conflicts")]
print(len(hits) > 0)
' 2>/dev/null || true)
  [[ "$out" == "True" ]] && { conflict=1; break; }
  status=$(run_status "$RUN2_ID" || true)
  [[ "$status" == published ]] && { show_tail "$RUN2_ID"; fail "同版本重发居然 published：拦截失效"; }
  sleep 3
done
if (( conflict == 0 )); then
  show_tail "$RUN2_ID"
  fail "第二次运行没有出现 version_conflicts 事件（拦截未生效）"
fi
pass "第二次运行出现 version_conflicts（同版本换字节已拦截）"

status=$(run_status "$RUN2_ID" || true)
[[ "$status" != published ]] || fail "第二次运行最终仍是 published"
pass "第二次运行状态为 $status（未发布）"

digest_after=$(curl -fsS "$UPDATE_URL/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for rel in (d.get("releases") or {}).values():
    if str(rel.get("version")) != sys.argv[1]:
        continue
    for a in rel.get("artifacts") or []:
        if a.get("slot") == "app":
            print(a.get("sha256")); raise SystemExit(0)
raise SystemExit(1)
' "$VERSION") || fail "第二次运行后读不到 app 槽位摘要"
[[ "$digest_after" == "$digest_before" ]] || fail "线上同版本槽位被改写：${digest_before:0:12} → ${digest_after:0:12}"
pass "线上同版本槽位摘要未被改写"

echo "VERSION_GATE_OK run=$RUN2_ID"
