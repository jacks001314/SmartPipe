#!/bin/bash
# release-v2-chain-drill.sh：用真实项目 + 真实构建，在替身升级服务 / 替身上跑
# 一次「三输入 → published」的无人值守全链路，不碰线上渠道。
#
# 与 failover-drill 的区别：不建合成项目，直接拿目标项目的现成发版配置和构建机，
# 因此校验的是真实制品矩阵（Syntropy 是 28 个产品槽位 + 2 个内容组件）。
#
# 用法：
#   sudo CHAIN_PROJECT_ID=prj_xxx bash scripts/release-v2-chain-drill.sh
# 退出时一定恢复发布设置并停掉替身。
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:8092}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/opt/smartpipe/deploy/.env}
NETWORK=${CHAIN_NETWORK:-smartpipe_default}
PRIMARY_PORT=${CHAIN_PRIMARY_PORT:-18200}
BACKUP_PORT=${CHAIN_BACKUP_PORT:-18201}
GH_PORT=${CHAIN_GITHUB_PORT:-9199}
TIMEOUT=${CHAIN_TIMEOUT:-5400}
PROJECT_ID=${CHAIN_PROJECT_ID:?需要 CHAIN_PROJECT_ID}
EXECUTOR=${CHAIN_EXECUTOR:-mac-01}
CRED=${CHAIN_UPDATE_CRED:-drill-token}
GH_CRED=${CHAIN_GITHUB_CRED:-ghp_fake_rehearsal}
FAKE_DIR=${CHAIN_FAKE_DIR:-/home/jacks/fake-update}
STAMP=$(date +%s)
WORK_HOST=${CHAIN_WORK_DIR:-/opt/smartpipe/deploy/data/drill/chain-$STAMP}

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
jf() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }
api() { curl -fsS -H "$AUTH" -H "$CT" "$@"; }

[[ $EUID -eq 0 ]] || fail "需要 root（要改发布设置、起替身服务）"
if [[ -z "${SMARTPIPE_ADMIN_TOKEN:-}" ]]; then
  [[ -r "$ENV_FILE" ]] || fail "缺少 SMARTPIPE_ADMIN_TOKEN"
  set -a; . "$ENV_FILE"; set +a
fi
AUTH="Authorization: Bearer ${SMARTPIPE_ADMIN_TOKEN}"
CT="Content-Type: application/json"

GW=$(docker network inspect "$NETWORK" | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["IPAM"]["Config"][0]["Gateway"])') || fail "取不到 $NETWORK 网关"
PRIMARY_URL="http://$GW:$PRIMARY_PORT"
BACKUP_URL="http://$GW:$BACKUP_PORT"
GH_URL="http://$GW:$GH_PORT"

mkdir -p "$FAKE_DIR" "$WORK_HOST"
SETTINGS_BACKUP="$WORK_HOST/settings.backup.json"
STARTED=()
start_update() {
  local port=$1 name=$2
  ( cd /opt/smartpipe && nohup python3 scripts/fake-update-service.py --host 0.0.0.0 --port "$port" > "$FAKE_DIR/chain-$name.log" 2>&1 & echo $! > "$FAKE_DIR/chain-$name.pid" )
  STARTED+=("$FAKE_DIR/chain-$name.pid")
  for _ in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  fail "假升级服务 $port 没起来（$FAKE_DIR/chain-$name.log）"
}
start_github() {
  ( cd /opt/smartpipe && FAKE_GITHUB_HOST=0.0.0.0 nohup node scripts/fake-github.mjs "$GH_PORT" > "$FAKE_DIR/chain-github.log" 2>&1 & echo $! > "$FAKE_DIR/chain-github.pid" )
  STARTED+=("$FAKE_DIR/chain-github.pid")
  for _ in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:$GH_PORT/__state" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  fail "假 GitHub 没起来（$FAKE_DIR/chain-github.log）"
}
stop_started() {
  for pidfile in "${STARTED[@]:-}"; do
    [[ -f "$pidfile" ]] || continue
    kill "$(cat "$pidfile")" 2>/dev/null || true
    rm -f "$pidfile"
  done
}
restore_settings() {
  [[ -s "$SETTINGS_BACKUP" ]] || return 0
  curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$SETTINGS_BACKUP" "$ADMIN/admin/releases/v2/settings" >/dev/null \
    && echo "PASS: 发布设置已恢复" || echo "FAIL: 恢复发布设置失败（备份 $SETTINGS_BACKUP）" >&2
}
cleanup() { restore_settings; stop_started; }
trap cleanup EXIT

curl -fsS "$ADMIN/healthz" >/dev/null || fail "admin-api 不健康"
curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/settings" | python3 -c 'import json,sys; json.dump(json.load(sys.stdin)["data"], sys.stdout)' > "$SETTINGS_BACKUP"
chmod 600 "$SETTINGS_BACKUP"
python3 - "$SETTINGS_BACKUP" <<'PY' || fail "发布设置备份为空，拒绝在裸设置上演练"
import json,sys
sys.exit(0 if json.load(open(sys.argv[1])).get("update_services") else 1)
PY
pass "已备份发布设置"

start_update "$PRIMARY_PORT" primary
start_update "$BACKUP_PORT" backup
start_github
pass "替身就绪：primary=$PRIMARY_URL backup=$BACKUP_URL github=$GH_URL"

DRILL_SETTINGS="$WORK_HOST/settings.drill.json"
python3 - "$SETTINGS_BACKUP" "$PRIMARY_URL" "$BACKUP_URL" "$GH_URL" "$CRED" "$GH_CRED" > "$DRILL_SETTINGS" <<'PY'
import json, sys
src, primary, backup, gh, cred, gh_cred = sys.argv[1:7]
d = json.load(open(src))
d["update_services"] = [
    {"name": "chain-primary", "url": primary, "credential": cred, "enabled": True},
    {"name": "chain-backup", "url": backup, "credential": cred, "enabled": True},
]
d["update_url"] = primary
d["update_credential"] = cred
d["github_api_base"] = gh
d["github_credential"] = gh_cred
print(json.dumps(d))
PY
curl -fsS -X PUT -H "$AUTH" -H "$CT" --data-binary @"$DRILL_SETTINGS" "$ADMIN/admin/releases/v2/settings" >/dev/null || fail "切换发布设置失败"
pass "发布设置已切到 chain-primary / chain-backup + 假 GitHub（退出自动恢复）"

ensure_credential() {
  local kind=$1 name=$2 present
  present=$(curl -fsS -H "$AUTH" "$ADMIN/admin/credentials" | python3 -c "import json,sys; d=json.load(sys.stdin)['data']; print(any(c.get('name')=='$name' for c in d))")
  [[ "$present" == "True" ]] || curl -fsS -X POST -H "$AUTH" -H "$CT" -d "{\"kind\":\"$kind\",\"name\":\"$name\",\"value\":\"$name\"}" "$ADMIN/admin/credentials" >/dev/null
}
ensure_credential release_update "$CRED"
ensure_credential github_pat "$GH_CRED"

PREVIEW=$(api "$ADMIN/admin/releases/v2/projects/$PROJECT_ID/release-preview")
echo "$PREVIEW" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
print("PASS: 预览 version=%s tag=%s commit=%s blockers=%s" % (d.get("version"), d.get("tag"), (d.get("commit") or "")[:12], d.get("blockers")))
sys.exit(0 if not d.get("blockers") else 1)
' || fail "预览有阻塞项"

RUN=$(api -X POST -d "{\"project_id\":\"$PROJECT_ID\",\"executor_ids\":[\"$EXECUTOR\"],\"update_server_ids\":[\"chain-primary\",\"chain-backup\"],\"idempotency_key\":\"chain-$STAMP\"}" "$ADMIN/admin/releases/v2/runs")
RUN_ID=$(echo "$RUN" | jf 'd["data"]["id"]')
[[ "$RUN_ID" == rrun_* ]] || fail "创建运行失败：$RUN"
pass "运行已创建：$RUN_ID（version=$(echo "$RUN" | jf 'd["data"]["version"]')）"

run_status() { api "$ADMIN/admin/releases/v2/runs/$RUN_ID" 2>/dev/null | jf 'd["data"].get("run",d["data"])["status"]'; }
show_tail() {
  api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
for ev in (d.get("events") or [])[-12:]:
    print("  %s %s %s" % (ev.get("actor"), ev.get("kind"), (ev.get("summary") or "")[:140]))
' 2>/dev/null || true
}

deadline=$((SECONDS + TIMEOUT))
while (( SECONDS < deadline )); do
  status=$(run_status || true)
  [[ "$status" == "published" ]] && { pass "运行 published（$RUN_ID）"; break; }
  case "$status" in
    failed|blocked|cancelled) show_tail; fail "运行停在 $status（$RUN_ID）" ;;
  esac
  sleep 15
done
[[ "${status:-}" == "published" ]] || { show_tail; fail "等待 ${TIMEOUT}s 仍未 published（当前 ${status:-unknown}）"; }

# 回读：两台替身的产品发布必须带全 28 个槽位单元，内容组件齐备，渠道已推进。
assert_service() {
  local url=$1 label=$2
  curl -fsS "$url/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
rels=d.get("releases") or {}
prods=[r for r in rels.values() if r.get("component")=="product"]
if not prods:
    print("FAIL: %s 没有 product 发布" % sys.argv[1], file=sys.stderr); sys.exit(1)
prod=sorted(prods, key=lambda r: r.get("createdAtMs") or 0)[-1]
arts=prod.get("artifacts") or []
cells={(a.get("slot"),a.get("os"),a.get("arch")) for a in arts}
content=sorted(r.get("component")+"@"+str(r.get("version")) for r in rels.values() if r.get("component") in ("templates","skills"))
dists=d.get("distributions") or []
print("PASS: %s product=%s 制品=%d 槽位单元=%d 内容=%s 渠道=%s" % (
    sys.argv[1], prod.get("version"), len(arts), len(cells), content,
    [x.get("productVersion") for x in dists]))
sys.exit(0 if len(cells) >= 28 and len(content) == 2 and dists else 1)
' "$label" || fail "$label 读回不一致"
}
assert_service "$PRIMARY_URL" chain-primary
assert_service "$BACKUP_URL" chain-backup

curl -fsS "http://127.0.0.1:$GH_PORT/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
rels=d.get("releases") or []
rel=sorted(rels, key=lambda r: r.get("id") or 0)[-1]
assets=rel.get("assets") or []
print("PASS: GitHub tag=%s published=%s assets=%d" % (rel.get("tag_name"), rel.get("published"), len(assets)))
sys.exit(0 if rel.get("published") and assets else 1)
' || fail "GitHub 替身读回不一致"

api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
arts=d.get("artifacts") or []
gh=[p for p in (d.get("publications") or []) if p.get("provider")=="github"]
print("PASS: 制品 %d 个，GitHub 发布记录 %d 条" % (len(arts), len(gh)))
'
echo "CHAIN_OK run=$RUN_ID"
