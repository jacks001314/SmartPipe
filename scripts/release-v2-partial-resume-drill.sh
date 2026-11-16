#!/bin/bash
# release-v2-partial-resume-drill.sh：验证 partially_published 会被自动接管。
#
# 场景（全部在替身升级服务 / 替身 GitHub 上，不碰线上渠道）：
#   1. 用合成项目跑一次 publish，构建是秒级的假任务，GitHub 同步正常完成。
#   2. 在 Agent 调 release_publish 之前打开替身 GitHub 的 holdPublish，
#      让「渠道已推进、GitHub 发布失败」这条路径落到 partially_published。
#   3. 重启控制面，杀掉仍在重试的 Agent 会话（此时 run 停在 partially_published，
#      且不在启动清扫与 runner 的活跃状态里，旧实现会永久卡住）。
#   4. 放开 holdPublish，不做任何人工操作，断言 attention worker 重新接管，
#      run 自己走到 published，且事件链里出现 from=partially_published。
#
# 脚本结束（含失败退出）一定恢复原发布设置并停掉替身服务。
#
# 用法：
#   sudo bash scripts/release-v2-partial-resume-drill.sh
#
# 环境变量：
#   SMARTPIPE_ADMIN_URL / SMARTPIPE_ENV_FILE / DRILL_NETWORK
#   DRILL_UPDATE_PORT    默认 18300
#   DRILL_GITHUB_PORT    默认 9299
#   DRILL_EXECUTOR       默认 mac-01
#   DRILL_TIMEOUT        进入 partially_published 后等待 published 的秒数，默认 900
#   DRILL_WORK_DIR       默认 /opt/smartpipe/deploy/data/drill/partial-<stamp>
#   DRILL_MODEL_PROVIDER / DRILL_MODEL
#                        给演练项目的发布 Agent 指定模型供应商与模型。默认跟随
#                        全局默认模型；默认供应商余额不足时用它临时切到可用通道。
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:8092}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/opt/smartpipe/deploy/.env}
NETWORK=${DRILL_NETWORK:-smartpipe_default}
UPDATE_PORT=${DRILL_UPDATE_PORT:-18300}
GH_PORT=${DRILL_GITHUB_PORT:-9299}
EXECUTOR=${DRILL_EXECUTOR:-mac-01}
TIMEOUT=${DRILL_TIMEOUT:-900}
MODEL_PROVIDER=${DRILL_MODEL_PROVIDER:-}
MODEL=${DRILL_MODEL:-}
STAMP=$(date +%s)
WORK_HOST=${DRILL_WORK_DIR:-/opt/smartpipe/deploy/data/drill/partial-$STAMP}
case "$WORK_HOST" in
  /opt/smartpipe/deploy/data/*) WORK="/data/${WORK_HOST#/opt/smartpipe/deploy/data/}" ;;
  *) echo "FAIL: DRILL_WORK_DIR 必须在 /opt/smartpipe/deploy/data 下（容器挂载点）" >&2; exit 1 ;;
esac
APP_UID=${DRILL_APP_UID:-10001}
APP_GID=${DRILL_APP_GID:-10001}
CRED=drill-token
GH_CRED=ghp_fake_rehearsal
SERVICE=pr5-primary
APP_CONTAINER=${DRILL_APP_CONTAINER:-smartpipe-app-1}
FAKE_DIR=${DRILL_FAKE_DIR:-/home/jacks/fake-update}
TARGET=darwin-amd64

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
jf() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }
api() { curl -fsS -H "$AUTH" -H "$CT" "$@"; }

[[ $EUID -eq 0 ]] || fail "需要 root：要改发布设置、起替身服务、重启控制面"
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
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null || true
    sleep 1
  fi
}

start_update() {
  stop_listener "$UPDATE_PORT"
  ( cd /opt/smartpipe && nohup python3 scripts/fake-update-service.py --host 0.0.0.0 --port "$UPDATE_PORT" > "$FAKE_DIR/partial-update.log" 2>&1 & echo $! > "$FAKE_DIR/partial-update.pid" )
  STARTED+=("$FAKE_DIR/partial-update.pid")
  local i
  for i in $(seq 1 40); do
    curl -fsS --max-time 2 "http://127.0.0.1:$UPDATE_PORT/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  fail "替身升级服务 $UPDATE_PORT 没起来（$FAKE_DIR/partial-update.log）"
}

start_github() {
  stop_listener "$GH_PORT"
  ( cd /opt/smartpipe && FAKE_GITHUB_HOST=0.0.0.0 nohup node scripts/fake-github.mjs "$GH_PORT" > "$FAKE_DIR/partial-github.log" 2>&1 & echo $! > "$FAKE_DIR/partial-github.pid" )
  STARTED+=("$FAKE_DIR/partial-github.pid")
  local i
  for i in $(seq 1 40); do
    curl -fsS --max-time 2 "http://127.0.0.1:$GH_PORT/__state" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  fail "替身 GitHub 没起来（$FAKE_DIR/partial-github.log）"
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
d["update_services"] = [{"name": "pr5-primary", "url": update, "credential": cred, "enabled": True}]
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

github_hold() {
  curl -fsS -X POST -H "$CT" -d "{\"on\":$1,\"after\":0}" "http://127.0.0.1:$GH_PORT/__hold" >/dev/null
}

# 确定性构造 partial：替身 GitHub 从开始就拒绝「公开草稿」PATCH。
# github_release_sync 是直接建出已发布的 Release（不发 PATCH），所以全程唯一
# 会被拒绝的是 release_publish 收口时的那次 PublishRelease —— 正好落在
# finishPublish，得到「渠道已推进、GitHub 没收口」的 partially_published。
github_hold true
pass "替身 GitHub 已进入 holdPublish：收口时的发布 PATCH 会 503"

REPO="$WORK/repo"
REPO_HOST="$WORK_HOST/repo"
mkdir -p "$REPO_HOST"
git -C "$REPO_HOST" init -q -b main
printf '{"version":"0.9.70001"}\n' > "$REPO_HOST/version.json"
RECIPE_BUILD="mkdir -p dist && date -u > dist/app.txt && tar -czf dist/app.tar.gz dist/app.txt"
git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST" add -A
git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST" commit -qm "drill partial"
git -c safe.directory="$REPO_HOST" -c user.name=drill -c user.email=drill@example.invalid -C "$REPO_HOST" tag -f v0.9.70001 >/dev/null
chown -R "$APP_UID:$APP_GID" "$REPO_HOST"

EXTRA=""
[[ -n "$MODEL_PROVIDER" ]] && EXTRA="$EXTRA,\"codex_model_provider\":\"$MODEL_PROVIDER\""
[[ -n "$MODEL" ]] && EXTRA="$EXTRA,\"codex_model\":\"$MODEL\""
PROJECT=$(api -X POST -d "{\"name\":\"partial-$STAMP\",\"github_repo_url\":\"file://$REPO\",\"github_default_branch\":\"main\",\"local_repo_path\":\"$REPO\",\"enabled\":true$EXTRA}" "$ADMIN/admin/projects")
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

RUN=$(api -X POST -d "{\"project_id\":\"$PROJECT_ID\",\"update_server_ids\":[\"$SERVICE\"],\"executor_ids\":[\"$EXECUTOR\"],\"idempotency_key\":\"partial-$STAMP\"}" "$ADMIN/admin/releases/v2/runs")
RUN_ID=$(echo "$RUN" | jf 'd["data"]["id"]')
[[ "$RUN_ID" == rrun_* ]] || fail "创建运行失败：$RUN"
VERSION=$(echo "$RUN" | jf 'd["data"]["version"]')
pass "运行已创建：$RUN_ID（version=$VERSION）"

run_status() { api "$ADMIN/admin/releases/v2/runs/$RUN_ID" 2>/dev/null | jf 'd["data"].get("run",d["data"])["status"]'; }
show_tail() {
  api "$ADMIN/admin/releases/v2/runs/$RUN_ID" 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
for ev in (d.get("events") or [])[-12:]:
    print("  %s %s %s" % (ev.get("actor"), ev.get("kind"), (ev.get("summary") or "")[:140]))
' 2>/dev/null || true
}
wait_status() {
  local want=$1 limit=$2 deadline=$((SECONDS + $2)) status
  while (( SECONDS < deadline )); do
    status=$(run_status || true)
    [[ "$status" == "$want" ]] && { pass "运行状态 $want（$RUN_ID）"; return 0; }
    case "$status" in
      failed|blocked|cancelled) show_tail; fail "运行停在 $status，期望 $want（$RUN_ID）" ;;
    esac
    sleep 3
  done
  show_tail
  fail "等待 ${limit}s 仍未到 $want（当前 ${status:-unknown}，$RUN_ID）"
}

wait_status partially_published 600

# 渠道必须已经推进：partial 的定义就是「升级服务发了、GitHub 没发」。
curl -fsS "$UPDATE_URL/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
dists=d.get("distributions") or []
versions=[x.get("productVersion") for x in dists]
sys.exit(0 if sys.argv[1] in versions else 1)
' "$VERSION" || fail "渠道指针没有推进到 $VERSION，不是真实的 partial 状态"
pass "替身渠道已推进到 $VERSION（符合 partially_published 定义）"

# 2) 杀掉仍在重试的 Agent 会话：重启控制面。启动清扫不会动 partial，
#    旧实现到这里就永久卡住了。
docker restart "$APP_CONTAINER" >/dev/null
for _ in $(seq 1 60); do
  curl -fsS "$ADMIN/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$ADMIN/healthz" >/dev/null || fail "控制面重启后不健康"
status=$(run_status || true)
[[ "$status" == partially_published ]] || fail "重启后运行状态被改成 $status，期望保持 partially_published"
pass "控制面已重启，运行仍是 partially_published（没有人工介入）"

# 3) 放开 GitHub，什么都不做，等 attention worker 自己接管。
github_hold false
pass "已放开 GitHub，等待自动接管"

wait_status published "$TIMEOUT"

api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
events=d.get("events") or []
resumed=[e for e in events if e.get("kind")=="agent.attention_resume" and ((e.get("detail") or {}).get("from")=="partially_published")]
if not resumed:
    print("FAIL: 没有 agent.attention_resume(from=partially_published) 事件", file=sys.stderr); sys.exit(1)
print("PASS: 事件链出现 agent.attention_resume（from=partially_published，attempt=%s）" % (resumed[-1].get("detail") or {}).get("attempt"))
' || fail "自动接管事件缺失：published 不是由 attention 拿下的"

api "$ADMIN/admin/releases/v2/runs/$RUN_ID" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]
events=d.get("events") or []
threads=[e for e in events if e.get("kind")=="agent.thread"]
reused=[e for e in threads if (e.get("detail") or {}).get("resumed") is True]
if not reused:
    ids=[(e.get("detail") or {}).get("thread_id") for e in threads]
    print("FAIL: 没有 resumed=true 的 agent.thread 事件（会话没有复用线程 %s）" % ids, file=sys.stderr); sys.exit(1)
print("PASS: 会话复用同一 Codex 线程（thread_id=%s，resumed 次数=%d）" % ((reused[-1].get("detail") or {}).get("thread_id"), len(reused)))
' || fail "线程复用断言失败"

curl -fsS "$UPDATE_URL/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
dists=[x for x in (d.get("distributions") or []) if x.get("productVersion")==sys.argv[1]]
if not dists:
    print("FAIL: 替身渠道没有 %s 的发行记录" % sys.argv[1], file=sys.stderr); sys.exit(1)
print("PASS: 替身渠道 %s BOM 已落库（components=%d）" % (sys.argv[1], len(dists[-1].get("components") or [])))
' "$VERSION" || fail "渠道发行记录缺失"

curl -fsS "http://127.0.0.1:$GH_PORT/__state" | python3 -c '
import json,sys
d=json.load(sys.stdin)
rels=[r for r in (d.get("releases") or []) if r.get("published")]
if not rels:
    print("FAIL: GitHub 没有已发布的 release", file=sys.stderr); sys.exit(1)
rel=sorted(rels, key=lambda r: r.get("id") or 0)[-1]
if not (rel.get("assets") or []):
    print("FAIL: GitHub release 没有资产", file=sys.stderr); sys.exit(1)
print("PASS: GitHub release tag=%s published=%s assets=%d" % (rel.get("tag_name"), rel.get("published"), len(rel.get("assets") or [])))
' || fail "GitHub 读回不一致"

echo "PARTIAL_RESUME_OK run=$RUN_ID"
