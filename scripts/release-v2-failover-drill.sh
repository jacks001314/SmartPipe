#!/bin/bash
# release-v2-failover-drill.sh：P4 故障隔离实机演练（主 / 备两台升级服务 + GitHub）。
#
# 场景：
#   1. 基线：三输入发布一次，两台升级服务都拿到同一版本，GitHub Release 可回读。
#   2. 故障：主库的前 N 次制品上传返回 503，备库成功；Agent 只重试故障台，主库重试成功。
#   3. 收口：运行 published，两台读回版本一致，GitHub Release 已发布且附件摘要可回读。
#
# 脚本结束（含失败退出）一定会恢复原发布设置并停掉本脚本启动的替身服务。
#
# 用法：
#   sudo bash scripts/release-v2-failover-drill.sh
#
# 环境变量：
#   SMARTPIPE_ADMIN_URL        默认 http://127.0.0.1:8092
#   SMARTPIPE_ENV_FILE         默认 /opt/smartpipe/deploy/.env（取 SMARTPIPE_ADMIN_TOKEN）
#   DRILL_NETWORK              默认 smartpipe_default（用它的网关地址给容器回连替身）
#   DRILL_PRIMARY_PORT         默认 18100
#   DRILL_BACKUP_PORT          默认 18101
#   DRILL_GITHUB_PORT          默认 9099
#   DRILL_TARGET               默认 darwin-amd64
#   DRILL_EXECUTOR             默认 mac-01
#   DRILL_TIMEOUT              单个运行等待秒数，默认 2400
#   DRILL_WORK_DIR             默认 /opt/smartpipe/deploy/data/drill/failover-<stamp>
#                              （必须落在容器挂载的 ./data 下：容器里是 /data）
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:8092}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/opt/smartpipe/deploy/.env}
NETWORK=${DRILL_NETWORK:-smartpipe_default}
PRIMARY_PORT=${DRILL_PRIMARY_PORT:-18100}
BACKUP_PORT=${DRILL_BACKUP_PORT:-18101}
GH_PORT=${DRILL_GITHUB_PORT:-9099}
TARGET=${DRILL_TARGET:-darwin-amd64}
EXECUTOR=${DRILL_EXECUTOR:-mac-01}
TIMEOUT=${DRILL_TIMEOUT:-2400}
STAMP=$(date +%s)
# 服务端在容器里读源码，所以仓库必须放在挂载卷内：宿主机 /opt/smartpipe/deploy/data
# 对应容器 /data。WORK 是容器路径，WORK_HOST 是宿主机路径。
WORK_HOST=${DRILL_WORK_DIR:-/opt/smartpipe/deploy/data/drill/failover-$STAMP}
case "$WORK_HOST" in
  /opt/smartpipe/deploy/data/*) WORK="/data/${WORK_HOST#/opt/smartpipe/deploy/data/}" ;;
  *) echo "FAIL: DRILL_WORK_DIR 必须在 /opt/smartpipe/deploy/data 下（容器挂载点）" >&2; exit 1 ;;
esac
APP_UID=${DRILL_APP_UID:-10001}
APP_GID=${DRILL_APP_GID:-10001}
CRED=drill-token
GH_CRED=ghp_fake_rehearsal
FAKE_DIR=/home/jacks/fake-update

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
jf() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }
api() { curl -fsS -H "$AUTH" -H "$CT" "$@"; }

[[ $EUID -eq 0 ]] || fail "需要 root：要写发布设置、启动替身服务"

if [[ -z "${SMARTPIPE_ADMIN_TOKEN:-}" ]]; then
  [[ -r "$ENV_FILE" ]] || fail "缺少 SMARTPIPE_ADMIN_TOKEN，且无法读取 $ENV_FILE"
  set -a; . "$ENV_FILE"; set +a
fi
[[ -n "${SMARTPIPE_ADMIN_TOKEN:-}" ]] || fail "$ENV_FILE 里没有 SMARTPIPE_ADMIN_TOKEN"
AUTH="Authorization: Bearer ${SMARTPIPE_ADMIN_TOKEN}"
CT="Content-Type: application/json"

GW=$(docker network inspect "$NETWORK" | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["IPAM"]["Config"][0]["Gateway"])') || fail "取不到 $NETWORK 网关"
[[ -n "$GW" ]] || fail "取不到 $NETWORK 网关"
PRIMARY_URL="http://$GW:$PRIMARY_PORT"
BACKUP_URL="http://$GW:$BACKUP_PORT"
GH_URL="http://$GW:$GH_PORT"

pass "admin-api 健康：$(curl -fsS "$ADMIN/healthz" | jf 'd.get("version","")')"
pass "替身地址：primary=$PRIMARY_URL backup=$BACKUP_URL github=$GH_URL"

mkdir -p "$FAKE_DIR" "$WORK_HOST"
PRIMARY_STARTED=0 BACKUP_STARTED=0 GH_STARTED=0
ORPHAN_CREDS=()
SETTINGS_BACKUP="$WORK_HOST/settings.backup.json"
chmod 700 "$WORK_HOST"
# 容器以 10001 运行，要能穿过工作目录读到仓库。
chown "$APP_UID:$APP_GID" "$WORK_HOST"

# stop_listener 结束占用演练端口的旧进程；这些端口只给替身用。
stop_listener() {
  local port=$1 pids
  pids=$(lsof -ti "tcp:$port" 2>/dev/null || true)
  if [[ -n "$pids" ]]; then
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null || true
    sleep 1
  fi
}

start_update() {
  local port=$1 name=$2
  # 端口上可能是上一轮演练残留的旧版本替身：它会让新接口看起来“不存在”。
  # 这些端口是演练专用，直接清掉再用当前脚本启动。
  stop_listener "$port"
  ( cd /opt/smartpipe && nohup python3 scripts/fake-update-service.py --host 0.0.0.0 --port "$port" > "$FAKE_DIR/$name.log" 2>&1 & echo $! > "$FAKE_DIR/$name.pid" )
  local i
  for i in $(seq 1 40); do
    curl -fsS --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  fail "假升级服务 $port 没起来（日志 $FAKE_DIR/$name.log）"
}

start_github() {
  stop_listener "$GH_PORT"
  ( cd /opt/smartpipe && FAKE_GITHUB_HOST=0.0.0.0 nohup node scripts/fake-github.mjs "$GH_PORT" > "$FAKE_DIR/github.log" 2>&1 & echo $! > "$FAKE_DIR/github.pid" )
  local i
  for i in $(seq 1 40); do
    curl -s --max-time 2 "http://127.0.0.1:$GH_PORT/__state" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  fail "假 GitHub 没起来（日志 $FAKE_DIR/github.log）"
}

stop_started() {
  local name pid
  for name in primary backup github; do
    case "$name:$PRIMARY_STARTED:$BACKUP_STARTED:$GH_STARTED" in
      primary:1:*|backup:*:1:*|github:*:*:1) ;;
      *) continue ;;
    esac
    [[ -f "$FAKE_DIR/$name.pid" ]] || continue
    pid=$(cat "$FAKE_DIR/$name.pid")
    [[ "$pid" =~ ^[0-9]+$ ]] && kill "$pid" 2>/dev/null || true
    rm -f "$FAKE_DIR/$name.pid"
  done
  # pid 文件偶尔会记到子 shell 上；按端口再兜底一次，避免残留旧替身。
  stop_listener "$PRIMARY_PORT"
  stop_listener "$BACKUP_PORT"
  stop_listener "$GH_PORT"
}

restore_settings() {
  if [[ -s "$SETTINGS_BACKUP" ]]; then
    # 控制面重启期间写设置会失败；重试几轮，别把生产设置留在替身上。
    local i
    for i in $(seq 1 30); do
      if curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$SETTINGS_BACKUP" "$ADMIN/admin/releases/v2/settings" >/dev/null 2>&1; then
        echo "已恢复原发布设置"
        return 0
      fi
      sleep 2
    done
    echo "WARN: 恢复发布设置失败，备份在 $SETTINGS_BACKUP" >&2
  fi
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

# 设置接口返回 {"data": {...}} 包装，而 PUT 只认顶层字段；
# 备份和恢复都必须落到内层对象，否则会把生产设置写成空值。
api "$ADMIN/admin/releases/v2/settings" | python3 -c 'import json,sys; json.dump(json.load(sys.stdin)["data"], sys.stdout)' > "$SETTINGS_BACKUP"
chmod 600 "$SETTINGS_BACKUP"
python3 - "$SETTINGS_BACKUP" <<'PY' || fail "发布设置备份为空（update_services），拒绝在裸设置上做演练"
import json, sys
d = json.load(open(sys.argv[1]))
if not d.get("update_services"):
    sys.exit(1)
PY
pass "已备份当前发布设置：$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(",".join(s["name"] for s in d.get("update_services") or []))' "$SETTINGS_BACKUP")"

start_update "$PRIMARY_PORT" primary && PRIMARY_STARTED=1
start_update "$BACKUP_PORT" backup && BACKUP_STARTED=1
start_github && GH_STARTED=1
pass "替身服务就绪（primary / backup / github）"

DRILL_SETTINGS="$WORK_HOST/settings.drill.json"
python3 - "$SETTINGS_BACKUP" "$PRIMARY_URL" "$BACKUP_URL" "$GH_URL" "$CRED" "$GH_CRED" > "$DRILL_SETTINGS" <<'PY'
import json, sys
src, primary, backup, gh, cred, gh_cred = sys.argv[1:7]
d = json.load(open(src))
d["update_services"] = [
    {"name": "p4-primary", "url": primary, "credential": cred, "enabled": True},
    {"name": "p4-backup", "url": backup, "credential": cred, "enabled": True},
]
d["update_url"] = primary
d["update_credential"] = cred
d["github_api_base"] = gh
d["github_credential"] = gh_cred
print(json.dumps(d))
PY
curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$DRILL_SETTINGS" "$ADMIN/admin/releases/v2/settings" >/dev/null || fail "切换发布设置到替身失败"
pass "发布设置已切到 p4-primary / p4-backup + 假 GitHub（退出时自动恢复）"

# 替身不校验令牌，但 release-mcp 会按名字解析凭据，所以先把两个名字登记上。
ensure_credential() {
  local kind=$1 name=$2 present
  present=$(api "$ADMIN/admin/credentials" 2>/dev/null | jf "any(c.get('name')==\"$name\" for c in d['data'])")
  if [[ "$present" != "True" ]]; then
    api -X POST -d "{\"kind\":\"$kind\",\"name\":\"$name\",\"value\":\"$name\"}" "$ADMIN/admin/credentials" >/dev/null || fail "登记凭据 $name 失败"
    ORPHAN_CREDS+=("$name")
    pass "已登记演练用凭据（$name）"
  fi
}
ensure_credential github_pat "$GH_CRED"
ensure_credential release_update "$CRED"

REPO="$WORK/repo"
REPO_HOST="$WORK_HOST/repo"
mkdir -p "$REPO_HOST"
git -C "$REPO_HOST" init -q -b main
RECIPE_BUILD="mkdir -p dist && date -u > dist/app.txt && tar -czf dist/app.tar.gz dist/app.txt"

bump_version() {
  local version=$1
  printf '{"version":"%s"}\n' "$version" > "$REPO_HOST/version.json"
  # 仓库归容器 uid(10001) 所有，root 再操作会被 git 判为 dubious ownership。
  local g=(git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST")
  "${g[@]}" add -A
  "${g[@]}" commit -qm "drill $version"
  "${g[@]}" tag -f "v$version" >/dev/null
  # 容器以 10001 运行；仓库归它所有，git archive 才不会报 dubious ownership。
  chown -R "$APP_UID:$APP_GID" "$REPO_HOST"
}

PROJECT=$(api -X POST -d "{\"name\":\"failover-$STAMP\",\"github_repo_url\":\"file://$REPO\",\"github_default_branch\":\"main\",\"local_repo_path\":\"$REPO\",\"enabled\":true}" "$ADMIN/admin/projects")
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
pass "项目发布配置已写入（$TARGET，product=fixture，slots app→app-bundle）"

create_run() {
  local version=$1 body
  body=$(python3 -c 'import json,sys;print(json.dumps({"project_id":sys.argv[1],"update_server_ids":["p4-primary","p4-backup"],"executor_ids":[sys.argv[2]],"idempotency_key":"failover-"+sys.argv[3]}))' "$PROJECT_ID" "$EXECUTOR" "$version-$STAMP")
  api -X POST -d "$body" "$ADMIN/admin/releases/v2/runs"
}

run_status() { api "$ADMIN/admin/releases/v2/runs/$RUN_ID" 2>/dev/null | jf 'd["data"].get("run",d["data"])["status"]'; }
run_events() { api "$ADMIN/admin/releases/v2/runs/$RUN_ID" 2>/dev/null | jf 'json.dumps(d["data"].get("events") or [])'; }

show_tail() {
  api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
for ev in (d.get("events") or [])[-10:]:
    print("  %s %s %s" % (ev.get("actor"), ev.get("kind"), (ev.get("summary") or "")[:150]))
' 2>/dev/null || true
}

wait_published() {
  local deadline=$((SECONDS + TIMEOUT)) status
  while (( SECONDS < deadline )); do
    status=$(run_status || true)
    [[ "$status" == "published" ]] && { pass "运行 published（$RUN_ID）"; return 0; }
    case "$status" in
      failed|blocked|cancelled) show_tail; fail "运行停在 $status（$RUN_ID）" ;;
    esac
    sleep 5
  done
  show_tail
  fail "等待 ${TIMEOUT}s 仍未 published（当前 ${status:-unknown}）"
}

wait_partial() {
  local deadline=$((SECONDS + TIMEOUT)) hit
  while (( SECONDS < deadline )); do
    hit=$(run_events | python3 -c '
import json,sys
evs=json.load(sys.stdin)
for ev in evs:
    if ev.get("kind")=="update_import.partial":
        print(json.dumps(ev.get("detail") or {}, ensure_ascii=False)); raise SystemExit(0)
raise SystemExit(1)
' 2>/dev/null) && { echo "$hit"; return 0; }
    case "$(run_status || true)" in
      failed|blocked|cancelled|published) show_tail; fail "未观察到 update_import.partial" ;;
    esac
    sleep 2
  done
  fail "等待 ${TIMEOUT}s 未观察到主库上传失败"
}

assert_server() {
  local url=$1 label=$2 version=$3 body tmp
  tmp=$(mktemp)
  body=$(curl -fsS "$url/v1/admin/distributions" 2>/dev/null || true)
  printf '%s' "$body" > "$tmp"
  python3 - "$tmp" "$label" "$version" <<'PY' || { rm -f "$tmp"; fail "$label 读回不一致"; }
import json, sys
path, label, version = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(path))
items = d.get("distributions") or []
hit = [x for x in items if str(x.get("productVersion")) == version]
if not hit:
    print("FAIL: %s 上没有版本 %s（现有 %s）" % (label, version, [x.get("productVersion") for x in items]), file=sys.stderr)
    sys.exit(1)
print("PASS: %s 读回版本 %s（channel=%s, bom=%s）" % (label, version, hit[-1].get("channel"), str(hit[-1].get("bomSha256"))[:19]))
PY
  rm -f "$tmp"
}

assert_github() {
  local version=$1 body tmp
  tmp=$(mktemp)
  body=$(curl -fsS "http://127.0.0.1:$GH_PORT/__state" 2>/dev/null || true)
  printf '%s' "$body" > "$tmp"
  python3 - "$tmp" "$version" <<'PY' || { rm -f "$tmp"; fail "GitHub 替身读回不一致"; }
import json, sys
path, version = sys.argv[1], sys.argv[2]
d = json.load(open(path))
rels = [r for r in (d.get("releases") or []) if r.get("tag_name") == "v" + version]
if not rels:
    print("FAIL: 没有 tag v%s 的 Release" % version, file=sys.stderr)
    sys.exit(1)
rel = rels[-1]
assets = rel.get("assets") or []
if not rel.get("published"):
    print("FAIL: Release 未发布（draft）", file=sys.stderr)
    sys.exit(1)
if not assets:
    print("FAIL: Release 没有附件", file=sys.stderr)
    sys.exit(1)
bad = [a for a in assets if not str(a.get("digest") or "").startswith("sha256:")]
if bad:
    print("FAIL: 附件缺少 sha256 摘要：%s" % [a.get("name") for a in bad], file=sys.stderr)
    sys.exit(1)
print("PASS: GitHub Release #%s v%s 已发布，附件 %d 个" % (rel.get("id"), version, len(assets)))
for a in assets:
    print("  - %s %s bytes %s" % (a.get("name"), a.get("size"), a.get("digest")))
PY
  rm -f "$tmp"
}

V1="0.9.$((STAMP % 100000))"
bump_version "$V1"
pass "=== 场景 1：基线发布 v$V1 ==="
RUN=$(create_run "$V1") || fail "创建运行失败：$RUN"
RUN_ID=$(echo "$RUN" | jf 'd["data"]["id"]')
[[ "$RUN_ID" == rrun_* ]] || fail "创建运行失败：$RUN"
pass "运行：$RUN_ID"
wait_published
assert_server "$BACKUP_URL" "p4-backup" "$V1"
assert_server "$PRIMARY_URL" "p4-primary" "$V1"
assert_github "$V1"

V2="0.9.$(((STAMP + 1) % 100000))"
bump_version "$V2"
pass "=== 场景 2：主库前 1 次上传失败，v$V2 ==="
curl -fsS -X POST -H "$CT" -d '{"on":true,"count":1}' "http://127.0.0.1:$PRIMARY_PORT/__fail_uploads" >/dev/null || fail "开启主库上传故障失败"
pass "主库已设置为下一次上传返回 503"

RUN=$(create_run "$V2") || fail "创建运行失败：$RUN"
RUN_ID=$(echo "$RUN" | jf 'd["data"]["id"]')
[[ "$RUN_ID" == rrun_* ]] || fail "创建运行失败：$RUN"
pass "运行：$RUN_ID"

PARTIAL=$(wait_partial) || fail "主库故障未产生 partial"
TMP=$(mktemp)
printf '%s' "$PARTIAL" > "$TMP"
python3 - "$TMP" <<'PY' || { rm -f "$TMP"; fail "partial 事件内容不符"; }
import json, sys
d = json.load(open(sys.argv[1]))
retry = d.get("retry_services") or []
print("PASS: 主库上传失败触发 state=partial，retry_services=%s" % retry)
if "p4-primary" not in retry:
    print("FAIL: retry_services 未包含 p4-primary", file=sys.stderr); sys.exit(1)
PY
rm -f "$TMP"

wait_published
# 升级服务只在渠道推进后才出现在发布列表里，所以回读必须放在 published 之后。
assert_server "$PRIMARY_URL" "p4-primary" "$V2"
assert_server "$BACKUP_URL" "p4-backup" "$V2"
assert_github "$V2"

run_events > "$TMP"
python3 - "$TMP" <<'PY' || { rm -f "$TMP"; fail "重试证据不足"; }
import json, sys
evs = json.load(open(sys.argv[1]))
kinds = [e.get("kind") for e in evs]
if kinds.count("update_import.partial") < 1:
    print("FAIL: 缺少 update_import.partial 事件", file=sys.stderr); sys.exit(1)
print("PASS: 事件链含 %d 次 partial、%d 次 update.import" % (
    kinds.count("update_import.partial"), kinds.count("update.import")))
PY
rm -f "$TMP"

echo "DRILL_OK primary=$PRIMARY_URL backup=$BACKUP_URL github=$GH_URL project=$PROJECT_ID baseline=$V1 failover=$V2 run=$RUN_ID"
