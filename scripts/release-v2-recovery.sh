#!/bin/bash
# release-v2-recovery.sh：中断恢复演练。
#
# 两个场景（参数 service / executor / both，默认 both）：
#   service   构建进行中重启 release-service，确认运行仍能收口到 built
#   executor  构建进行中 kill -9 执行器，确认租约到期后目标被重排、新一次尝试把构建跑完
#   storage   构建进行中拒绝对象存储端口，确认上传失败后目标重排、恢复后自动跑完
#   storage-exhaust  端口一直不通，确认尝试次数用尽后运行按 artifact_upload_failed 收口
#
# 用法：
#   sudo bash release-v2-recovery.sh            # 两个场景都跑
#   sudo bash release-v2-recovery.sh service
#   sudo bash release-v2-recovery.sh executor
#   sudo bash release-v2-recovery.sh storage
#   sudo bash release-v2-recovery.sh storage-exhaust
#
# 依赖：/opt/smartpipe-v2-test/fixture-slow（build 里 sleep 20），systemd 单元
#       smartpipe-v2-release 与 smartpipe-v2-executor 由 systemd 管理。
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:18092}
RELEASE=${SMARTPIPE_RELEASE_URL:-http://127.0.0.1:18099}
MCP=${SMARTPIPE_MCP_URL:-http://127.0.0.1:18097/mcp}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/etc/smartpipe/smartpipe.env}
SLOW_DIR=${SMOKE_SLOW_DIR:-/opt/smartpipe-v2-test/fixture-slow}
CHANNEL=${SMOKE_CHANNEL:-test}
TIMEOUT=${SMOKE_TIMEOUT:-300}
MODE=${1:-both}
S3_ADDR=${SMOKE_S3_ADDR:-127.0.0.1}
S3_PORT=${SMOKE_S3_PORT:-9000}

# 对象存储断开用 iptables 拒绝端口实现：比停 MinIO 更轻，退出时一定恢复。
S3_BLOCKED=0
block_s3() {
  iptables -I OUTPUT 1 -p tcp -d "$S3_ADDR" --dport "$S3_PORT" -j REJECT || fail "无法添加 iptables 规则"
  S3_BLOCKED=1
}
unblock_s3() {
  if [[ "$S3_BLOCKED" == "1" ]]; then
    iptables -D OUTPUT -p tcp -d "$S3_ADDR" --dport "$S3_PORT" -j REJECT 2>/dev/null || true
    S3_BLOCKED=0
  fi
}
trap unblock_s3 EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
json_field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }

case "$MODE" in service|executor|storage|storage-exhaust|both) ;; *) fail "参数只能是 service/executor/storage/storage-exhaust/both（当前 $MODE）" ;; esac
[[ $EUID -eq 0 ]] || fail "需要 root：要重启 systemd 单元"

if [[ -z "${SMARTPIPE_ADMIN_TOKEN:-}" ]]; then
  [[ -r "$ENV_FILE" ]] || fail "缺少 SMARTPIPE_ADMIN_TOKEN，且无法读取 $ENV_FILE"
  set -a; . "$ENV_FILE"; set +a
fi
[[ -n "${SMARTPIPE_ADMIN_TOKEN:-}" ]] || fail "$ENV_FILE 里没有 SMARTPIPE_ADMIN_TOKEN"
AUTH="Authorization: Bearer ${SMARTPIPE_ADMIN_TOKEN}"
CT="Content-Type: application/json"

[[ -d "$SLOW_DIR/.git" ]] || fail "$SLOW_DIR 不是 git 仓库"
COMMIT=$(git -c safe.directory="$SLOW_DIR" -C "$SLOW_DIR" rev-parse HEAD)

admin_version=$(curl -fsS "$ADMIN/healthz" | json_field 'd.get("version","")')
[[ -n "$admin_version" ]] || fail "admin-api /healthz 不可用"
pass "admin-api 健康：$admin_version"

# create_run <标签>：建项目/计划/运行，回显 run id
create_run() {
  local tag=$1 stamp project plan run run_id
  stamp=$(date +%s)$RANDOM
  project=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
    -d "{\"name\":\"recovery-$tag-$stamp\",\"github_repo_url\":\"file://$SLOW_DIR\",\"github_default_branch\":\"main\",\"local_repo_path\":\"$SLOW_DIR\",\"enabled\":true}" \
    "$ADMIN/admin/projects")
  local project_id
  project_id=$(echo "$project" | json_field 'd["data"]["id"]')
  [[ "$project_id" == prj_* ]] || fail "创建项目失败：$project"

  plan=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
    -d "{\"project_id\":\"$project_id\",\"version\":\"0.2.$((stamp % 100000))\",\"mode\":\"build_only\",\"channel\":\"$CHANNEL\",\"commit\":\"$COMMIT\"}" \
    "$ADMIN/admin/releases/v2/plans")
  local plan_id blockers
  plan_id=$(echo "$plan" | json_field 'd["data"]["id"]')
  [[ "$plan_id" == rplan_* ]] || fail "创建计划失败：$plan"
  blockers=$(echo "$plan" | json_field 'd["data"].get("blockers")')
  [[ "$blockers" == "None" || "$blockers" == "[]" ]] || fail "计划有阻塞项：$blockers"

  run=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
    -d "{\"plan_id\":\"$plan_id\",\"idempotency_key\":\"recovery-$stamp\"}" \
    "$ADMIN/admin/releases/v2/runs")
  run_id=$(echo "$run" | json_field 'd["data"]["id"]')
  [[ "$run_id" == rrun_* ]] || fail "创建运行失败：$run"
  pass "运行：$run_id（项目 $project_id）"
  echo "$run_id"
}

run_detail() { curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$1"; }

# mcp_tool <工具名> <参数JSON>：用运行级 token 调 release MCP，失败即退出。
mcp_tool() {
  local name=$1 args=${2:-"{}"} body out
  [[ -n "${TOKEN:-}" ]] || fail "没有运行级 token"
  body=$(python3 -c "import json,sys;print(json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':sys.argv[1],'arguments':json.loads(sys.argv[2])}}))" "$name" "$args")
  out=$(mktemp)
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "$CT" -d "$body" "$MCP" > "$out" || { rm -f "$out"; fail "MCP $name 请求失败"; }
  python3 - "$out" "$name" <<'PY' || { rm -f "$out"; exit 1; }
import json, sys
d = json.load(open(sys.argv[1]))
res = d.get("result") or {}
if d.get("error"):
    print("FAIL: %s rpc error %s" % (sys.argv[2], d["error"]), file=sys.stderr)
    sys.exit(1)
code = (res.get("structuredContent") or {}).get("code", "")
print("%s -> isError=%s code=%s" % (sys.argv[2], res.get("isError"), code or "-"))
if res.get("isError"):
    sys.exit(1)
PY
  rm -f "$out"
}
run_status() { run_detail "$1" | json_field 'd["data"].get("run",d["data"])["status"]'; }
target_attempt() { run_detail "$1" | json_field 'next((t.get("attempt") for t in (d["data"].get("targets") or [])), 0)'; }
target_status() { run_detail "$1" | json_field 'next((t.get("status") for t in (d["data"].get("targets") or [])), "")'; }

# wait_target <run> <期望目标状态> <秒数>：构建期间目标停在 leased，用它判断「执行器已认领」
wait_target() {
  local run_id=$1 want=$2 limit=$3 deadline=$((SECONDS + $3)) cur
  while (( SECONDS < deadline )); do
    cur=$(target_status "$run_id")
    [[ "$cur" == "$want" ]] && return 0
    sleep 1
  done
  fail "等待 $limit 秒目标仍未进入 $want（当前 $(target_status "$run_id")）"
}

wait_run() {
  local run_id=$1 want=$2 limit=$3 deadline=$((SECONDS + $3)) cur
  while (( SECONDS < deadline )); do
    cur=$(run_status "$run_id")
    [[ "$cur" == "$want" ]] && return 0
    case "$cur" in failed|blocked|cancelled) fail "运行停在 $cur" ;; esac
    sleep 2
  done
  fail "等待 $limit 秒运行仍未 $want（当前 $(run_status "$run_id")）"
}

assert_built_with_artifact() {
  local run_id=$1 detail arts
  detail=$(run_detail "$run_id")
  arts=$(echo "$detail" | json_field 'len(d["data"].get("artifacts") or [])')
  [[ "$arts" -ge 1 ]] || fail "运行 $run_id 没有制品"
  pass "运行 $run_id 到 built，制品 $arts 个"
}

wait_health() {
  local limit=$1 deadline=$((SECONDS + $1))
  while (( SECONDS < deadline )); do
    if curl -fs -m 3 "$RELEASE/healthz" 2>/dev/null | json_field 'd.get("status","")' | grep -q ok; then return 0; fi
    sleep 1
  done
  fail "等待 $limit 秒 release-service 仍未就绪"
}

scenario_service() {
  pass "场景 A：构建中重启 release-service"
  local run_id
  run_id=$(create_run svc | tail -1)
  wait_run "$run_id" building 120
  pass "运行已进入 building（目标已认领），重启 release-service"
  systemctl restart smartpipe-v2-release
  wait_health 60
  pass "release-service 已重启并就绪"
  wait_run "$run_id" built "$TIMEOUT"
  assert_built_with_artifact "$run_id"
  echo "RECOVERY_OK scenario=service run=$run_id"
}

scenario_executor() {
  pass "场景 B：构建中杀掉执行器，靠租约到期重试"
  local run_id pid attempt
  run_id=$(create_run exe | tail -1)
  wait_target "$run_id" leased 120
  pass "目标已被执行器认领（leased），构建进行中"
  pid=$(systemctl show -p MainPID --value smartpipe-v2-executor)
  [[ "$pid" =~ ^[0-9]+$ && "$pid" -gt 0 ]] || fail "拿不到执行器 PID"
  kill -9 "$pid"
  pass "已 kill -9 执行器（PID $pid），构建中断"
  wait_run "$run_id" built "$TIMEOUT"
  attempt=$(target_attempt "$run_id")
  [[ "$attempt" -ge 2 ]] || fail "目标 attempt=$attempt，未发生重试"
  pass "租约到期后目标重排，第 $attempt 次尝试构建成功"
  assert_built_with_artifact "$run_id"
  systemctl is-active --quiet smartpipe-v2-executor || fail "执行器未自动拉起"
  pass "执行器已由 systemd 自动拉起"
  echo "RECOVERY_OK scenario=executor run=$run_id attempt=$attempt"
}

scenario_storage() {
  pass "场景 C：构建中对象存储不可用（$S3_ADDR:$S3_PORT REJECT）"
  local run_id attempt_before cur deadline retries
  run_id=$(create_run sto | tail -1)
  wait_target "$run_id" leased 120
  attempt_before=$(target_attempt "$run_id")
  pass "目标已被认领（attempt=$attempt_before），8 秒后断开对象存储"
  sleep 8
  block_s3
  pass "对象存储不可用，等第一次上传失败并重排"

  deadline=$((SECONDS + 120))
  while (( SECONDS < deadline )); do
    cur=$(target_attempt "$run_id")
    [[ "$cur" -gt "$attempt_before" ]] && break
    sleep 2
  done
  cur=$(target_attempt "$run_id")
  [[ "$cur" -gt "$attempt_before" ]] || fail "上传失败后目标没有重排（attempt 仍为 $attempt_before）"
  pass "上传失败后目标已重排（attempt=$cur）"
  unblock_s3
  pass "对象存储已恢复"

  wait_run "$run_id" built "$TIMEOUT"
  assert_built_with_artifact "$run_id"
  retries=$(run_detail "$run_id" | json_field 'sum(1 for e in (d["data"].get("events") or []) if e.get("kind")=="target.retry")')
  [[ "${retries:-0}" -ge 1 ]] || fail "运行里没有 target.retry 事件"
  pass "共记录 $retries 次可重试失败（target.retry）"
  echo "RECOVERY_OK scenario=storage run=$run_id attempt=$cur"
}

[[ "$MODE" == "service" || "$MODE" == "both" ]] && scenario_service
[[ "$MODE" == "executor" || "$MODE" == "both" ]] && scenario_executor
scenario_storage_exhaust() {
  pass "场景 D：对象存储持续不可用，尝试次数用尽后收口"
  local run_id attempt_before status code attempts retries deadline
  run_id=$(create_run stx | tail -1)
  wait_target "$run_id" leased 120
  attempt_before=$(target_attempt "$run_id")
  pass "目标已被认领（attempt=$attempt_before），8 秒后断开对象存储并保持"
  sleep 8
  block_s3

  deadline=$((SECONDS + TIMEOUT))
  status=""
  while (( SECONDS < deadline )); do
    status=$(run_status "$run_id")
    [[ "$status" == "failed" ]] && break
    case "$status" in built|published) fail "对象存储一直不可用却完成了（$status）" ;; esac
    sleep 3
  done
  [[ "$status" == "failed" ]] || fail "等待 $TIMEOUT 秒运行仍未 failed（当前 $status）"
  pass "运行按预期 failed（不再无限重试）"
  unblock_s3
  pass "对象存储已恢复"

  code=$(run_detail "$run_id" | json_field 'next((t.get("error_code") for t in (d["data"].get("targets") or [])), "")')
  attempts=$(target_attempt "$run_id")
  retries=$(run_detail "$run_id" | json_field 'sum(1 for e in (d["data"].get("events") or []) if e.get("kind")=="target.retry")')
  [[ "$code" == "artifact_upload_failed" ]] || fail "目标 error_code=$code（期望 artifact_upload_failed）"
  pass "目标错误码：artifact_upload_failed"
  [[ "$attempts" -ge 3 ]] || fail "只尝试了 $attempts 次，未跑满上限"
  pass "共尝试 $attempts 次（上限 3）"
  [[ "${retries:-0}" -ge 2 ]] || fail "target.retry 事件只有 ${retries:-0} 条"
  pass "重试事件 ${retries} 条，最后一次失败后不再重排"
  local arts
  arts=$(run_detail "$run_id" | json_field 'len(d["data"].get("artifacts") or [])')
  [[ "$arts" == "0" ]] || fail "失败运行不应登记制品（实际 $arts）"
  pass "无制品"

  # 运维恢复存储后，用 release MCP 的 build_retry 重试失败目标，运行应重新跑完。
  TOKEN=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
    -d '{"scopes":["release.read","release.write","build.read","artifact.read"],"ttl_seconds":900}' \
    "$ADMIN/admin/releases/v2/runs/$run_id/agent-token" | json_field 'd["data"]["token"]')
  [[ "$TOKEN" == rmt_* ]] || fail "签发运行级 token 失败"
  pass "已签发运行级 token，调用 build_retry"
  mcp_tool build_retry '{"target":"linux-amd64","reason":"storage restored"}'
  pass "手工重试已受理"
  wait_run "$run_id" built "$TIMEOUT"
  assert_built_with_artifact "$run_id"
  echo "RECOVERY_OK scenario=storage-exhaust run=$run_id attempts=$attempts"
}

[[ "$MODE" == "storage" || "$MODE" == "both" ]] && scenario_storage
[[ "$MODE" == "storage-exhaust" ]] && scenario_storage_exhaust
echo "RECOVERY_DONE mode=$MODE"