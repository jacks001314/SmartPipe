#!/bin/bash
# release-v2-recovery.sh：中断恢复演练。
#
# 两个场景（参数 service / executor / both，默认 both）：
#   service   构建进行中重启 release-service，确认运行仍能收口到 built
#   executor  构建进行中 kill -9 执行器，确认租约到期后目标被重排、新一次尝试把构建跑完
#
# 用法：
#   sudo bash release-v2-recovery.sh            # 两个场景都跑
#   sudo bash release-v2-recovery.sh service
#   sudo bash release-v2-recovery.sh executor
#
# 依赖：/opt/smartpipe-v2-test/fixture-slow（build 里 sleep 20），systemd 单元
#       smartpipe-v2-release 与 smartpipe-v2-executor 由 systemd 管理。
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:18092}
RELEASE=${SMARTPIPE_RELEASE_URL:-http://127.0.0.1:18099}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/etc/smartpipe/smartpipe.env}
SLOW_DIR=${SMOKE_SLOW_DIR:-/opt/smartpipe-v2-test/fixture-slow}
CHANNEL=${SMOKE_CHANNEL:-test}
TIMEOUT=${SMOKE_TIMEOUT:-300}
MODE=${1:-both}

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
json_field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }

case "$MODE" in service|executor|both) ;; *) fail "参数只能是 service/executor/both（当前 $MODE）" ;; esac
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

[[ "$MODE" == "service" || "$MODE" == "both" ]] && scenario_service
[[ "$MODE" == "executor" || "$MODE" == "both" ]] && scenario_executor
echo "RECOVERY_DONE mode=$MODE"