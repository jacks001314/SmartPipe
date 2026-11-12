#!/bin/bash
# release-v2-smoke.sh：在 release-service 所在机器上跑发版 v2 冒烟。
#
# 模式（SMOKE_MODE）：
#   build   健康检查 → 建项目/计划/运行 → 等 built → 校验目标与制品摘要
#   stage   再走升级包 → 暂存 → 审批门禁 → 批准 → 推进渠道 → published
#   publish 再额外走 GitHub（草稿、附件、公开），断言两侧发布记录
#   agent   让服务端拉起真实 Release Agent 会话，等会话结束后断言运行 built
#   agent-stage   Agent 自主 build→打包→暂存，停在审批门面；脚本批准后断言自动续跑会话收口到 published
#   agent-publish 同上，但计划为 publish：批准后的续跑会话要完成 GitHub 发布并 published
#   fail    用必失败的 fixture 构建，断言运行 failed、有 error_message、不登记制品、不产生发布记录
#
# 用法：
#   sudo bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_MODE=stage bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_MODE=publish SMOKE_GITHUB_API_BASE=http://127.0.0.1:9099 bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_MODE=agent-stage bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_MODE=agent-publish SMOKE_GITHUB_API_BASE=http://127.0.0.1:9099 bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#   sudo SMOKE_MODE=fail bash /opt/smartpipe-v2-test/tools/release-v2-smoke.sh
#
# 环境变量：
#   SMARTPIPE_ADMIN_URL / SMARTPIPE_RELEASE_URL / SMARTPIPE_MCP_URL  接口地址
#   SMARTPIPE_ENV_FILE   默认 /etc/smartpipe/smartpipe.env（取 SMARTPIPE_ADMIN_TOKEN）
#   SMOKE_REPO_URL       默认 file:///opt/smartpipe-v2-test/fixture
#   SMOKE_PROFILE_DIR    默认 /opt/smartpipe-v2-test/fixture
#   SMOKE_MODE           build | stage | publish | agent | agent-stage | agent-publish | fail
#   SMOKE_AGENT_TIMEOUT  发布 Agent 单会话等待上限，默认 900 秒
#   SMOKE_CHANNEL        默认 test
#   SMOKE_ARTIFACT_ROLE  GitHub 上传的角色，默认 app-bundle
#   SMOKE_GITHUB_API_BASE 默认 http://127.0.0.1:9099（publish 模式回读用）
#   SMOKE_GITHUB_REPO     默认 smoke/fixture
#   SMOKE_TIMEOUT         默认 180 秒
set -euo pipefail

ADMIN=${SMARTPIPE_ADMIN_URL:-http://127.0.0.1:18092}
RELEASE=${SMARTPIPE_RELEASE_URL:-http://127.0.0.1:18099}
MCP=${SMARTPIPE_MCP_URL:-http://127.0.0.1:18097/mcp}
ENV_FILE=${SMARTPIPE_ENV_FILE:-/etc/smartpipe/smartpipe.env}
MODE=${SMOKE_MODE:-build}
DEFAULT_FIXTURE=/opt/smartpipe-v2-test/fixture
[[ "$MODE" == "fail" ]] && DEFAULT_FIXTURE=/opt/smartpipe-v2-test/fixture-fail
REPO_URL=${SMOKE_REPO_URL:-file://$DEFAULT_FIXTURE}
PROFILE_DIR=${SMOKE_PROFILE_DIR:-$DEFAULT_FIXTURE}
CHANNEL=${SMOKE_CHANNEL:-test}
ROLE=${SMOKE_ARTIFACT_ROLE:-app-bundle}
GH_BASE=${SMOKE_GITHUB_API_BASE:-http://127.0.0.1:9099}
GH_REPO=${SMOKE_GITHUB_REPO:-smoke/fixture}
TIMEOUT=${SMOKE_TIMEOUT:-180}
AGENT_TIMEOUT=${SMOKE_AGENT_TIMEOUT:-900}

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
json_field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null || true; }

if [[ -z "${SMARTPIPE_ADMIN_TOKEN:-}" ]]; then
  [[ -r "$ENV_FILE" ]] || fail "缺少 SMARTPIPE_ADMIN_TOKEN，且无法读取 $ENV_FILE"
  # shellcheck disable=SC1090
  set -a; . "$ENV_FILE"; set +a
fi
[[ -n "${SMARTPIPE_ADMIN_TOKEN:-}" ]] || fail "$ENV_FILE 里没有 SMARTPIPE_ADMIN_TOKEN"
case "$MODE" in build|stage|publish|agent|agent-stage|agent-publish|fail) ;; *) fail "SMOKE_MODE 只能是 build/stage/publish/agent/agent-stage/agent-publish/fail（当前 $MODE）" ;; esac
MODE_AGENT=0
[[ "$MODE" == agent || "$MODE" == agent-stage || "$MODE" == agent-publish ]] && MODE_AGENT=1

AUTH="Authorization: Bearer ${SMARTPIPE_ADMIN_TOKEN}"
CT="Content-Type: application/json"

admin_version=$(curl -fsS "$ADMIN/healthz" | json_field 'd.get("version","")')
[[ -n "$admin_version" ]] || fail "admin-api /healthz 不可用"
pass "admin-api 健康：$admin_version"
[[ "$(curl -fsS "$RELEASE/healthz" | json_field 'd.get("status","")')" == "ok" ]] || fail "release-service 健康检查失败"
pass "release-service 健康"

[[ -d "$PROFILE_DIR/.git" ]] || fail "$PROFILE_DIR 不是 git 仓库，无法解析提交"
COMMIT=$(git -c safe.directory="$PROFILE_DIR" -C "$PROFILE_DIR" rev-parse HEAD)
pass "构建提交：$COMMIT"

STAMP=$(date +%s)
PROJECT=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
  -d "{\"name\":\"smoke-$STAMP\",\"github_repo_url\":\"$REPO_URL\",\"github_default_branch\":\"main\",\"local_repo_path\":\"$PROFILE_DIR\",\"enabled\":true}" \
  "$ADMIN/admin/projects")
PROJECT_ID=$(echo "$PROJECT" | json_field 'd["data"]["id"]')
[[ "$PROJECT_ID" == prj_* ]] || fail "创建项目失败：$PROJECT"
pass "项目：$PROJECT_ID"

VERSION="0.0.$((STAMP % 100000))"
PLAN_MODE=build_only
case "$MODE" in stage|agent-stage) PLAN_MODE=stage ;; publish|agent-publish) PLAN_MODE=publish ;; esac
PLAN=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
  -d "{\"project_id\":\"$PROJECT_ID\",\"version\":\"$VERSION\",\"mode\":\"$PLAN_MODE\",\"channel\":\"$CHANNEL\",\"commit\":\"$COMMIT\"}" \
  "$ADMIN/admin/releases/v2/plans")
PLAN_ID=$(echo "$PLAN" | json_field 'd["data"]["id"]')
[[ "$PLAN_ID" == rplan_* ]] || fail "创建计划失败：$PLAN"
BLOCKERS=$(echo "$PLAN" | json_field 'd["data"].get("blockers")')
[[ "$BLOCKERS" == "None" || "$BLOCKERS" == "[]" ]] || fail "计划有阻塞项：$BLOCKERS"
pass "计划：$PLAN_ID（$VERSION，$PLAN_MODE）"

RUN=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
  -d "{\"plan_id\":\"$PLAN_ID\",\"idempotency_key\":\"smoke-$STAMP\"}" \
  "$ADMIN/admin/releases/v2/runs")
RUN_ID=$(echo "$RUN" | json_field 'd["data"]["id"]')
[[ "$RUN_ID" == rrun_* ]] || fail "创建运行失败：$RUN"
pass "运行：$RUN_ID"

# agent_launch 通过 admin API 拉起一次发布 Agent 会话，回显会话 ID。
agent_launch() {
  local resp sid
  resp=$(curl -fsS -X POST -H "$AUTH" -H "$CT" -d '{}' "$ADMIN/admin/releases/v2/runs/$RUN_ID/agent")
  sid=$(echo "$resp" | json_field 'd["data"]["id"]')
  [[ "$sid" == ragent_* ]] || fail "启动发布 Agent 失败：$resp"
  echo "$sid"
}

# agent_wait_finished 等会话结束，且要等到一个和 $1 不同的会话（自动续跑会新建会话）。
# 成功时回显 "<会话ID> <状态>"。
agent_wait_finished() {
  local prev=$1 deadline=$((SECONDS + AGENT_TIMEOUT)) a running sid status
  while (( SECONDS < deadline )); do
    a=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID/agent")
    running=$(echo "$a" | json_field 'd["data"].get("running")')
    sid=$(echo "$a" | json_field '(d["data"].get("session") or {}).get("id","")')
    status=$(echo "$a" | json_field '(d["data"].get("session") or {}).get("status","")')
    if [[ "$running" == "False" && -n "$sid" && "$sid" != "$prev" ]]; then
      echo "$sid $status"
      return 0
    fi
    sleep 3
  done
  fail "等待 $AGENT_TIMEOUT 秒发布 Agent 仍未结束（上一会话 ${prev:-无}）"
}

# assert_published 断言运行已 published、升级服务（和 publish 模式的 GitHub）发布记录一致。
assert_published() {
  local pub=$1 status gh_id gh_state gh_detail
  status=$(echo "$pub" | json_field 'd["data"].get("run",d["data"])["status"]')
  [[ "$status" == "published" ]] || fail "收口后未 published（当前 $status）"
  local pub_state
  pub_state=$(echo "$pub" | json_field 'next((p.get("state") for p in (d["data"].get("publications") or []) if p.get("provider")=="update_service"), "")')
  [[ "$pub_state" == "promoted" ]] || fail "update_service 发布记录状态=$pub_state"
  pass "运行已发布（update_service=promoted）"
  if [[ "$MODE" == "publish" || "$MODE" == "agent-publish" ]]; then
    gh_id=$(echo "$pub" | json_field 'next((p.get("external_id") for p in (d["data"].get("publications") or []) if p.get("provider")=="github"), "")')
    [[ -n "$gh_id" ]] || fail "缺少 GitHub 发布记录"
    gh_state=$(echo "$pub" | json_field 'next((p.get("state") for p in (d["data"].get("publications") or []) if p.get("provider")=="github"), "")')
    [[ "$gh_state" == "published" ]] || fail "GitHub 发布记录状态=$gh_state"
    gh_detail=$(curl -fsS -H "Authorization: Bearer smoke" "$GH_BASE/repos/$GH_REPO/releases/$gh_id")
    python3 - "$gh_detail" <<'PY' || fail "假 GitHub 回读不一致"
import json, sys
rel = json.loads(sys.argv[1])
assets = rel.get("assets") or []
if not rel.get("published"):
    print("FAIL: github release published=%s" % rel.get("published"), file=sys.stderr)
    sys.exit(1)
if not assets:
    print("FAIL: github release 没有附件", file=sys.stderr)
    sys.exit(1)
print("PASS: GitHub release %s published，附件 %d 个" % (rel.get("id"), len(assets)))
for a in assets:
    print("  - %s %s bytes" % (a.get("name"), a.get("size")))
PY
    pass "GitHub 回读一致（published + 附件）"
  fi
}

if [[ $MODE_AGENT -eq 1 ]]; then
  SESSION_ID=$(agent_launch)
  pass "发布 Agent 会话：$SESSION_ID"
  RESULT=$(agent_wait_finished "")
  SESSION_ID=${RESULT%% *}
  SESSION_STATUS=${RESULT##* }
  [[ "$SESSION_STATUS" == "finished" ]] || fail "发布 Agent 会话状态=$SESSION_STATUS"
  pass "发布 Agent 会话结束：finished（$SESSION_ID）"

  DETAIL=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID")
  STATUS=$(echo "$DETAIL" | json_field 'd["data"].get("run",d["data"])["status"]')
  case "$MODE" in
    agent)
      [[ "$STATUS" == "built" ]] || fail "Agent 结束后运行状态=$STATUS（期望 built）"
      pass "运行状态：built（Agent 收口）"
      ;;
    agent-stage|agent-publish)
      # Agent 应当在审批门禁前停下：升级包已暂存，运行停在 staged。
      [[ "$STATUS" == "staged" ]] || fail "Agent 结束后运行状态=$STATUS（期望停在 staged 等审批）"
      pass "运行状态：staged（Agent 停在审批门禁）"
      PUB_STATE_BEFORE=$(echo "$DETAIL" | json_field 'next((p.get("state") for p in (d["data"].get("publications") or []) if p.get("provider")=="update_service"), "")')
      [[ "$PUB_STATE_BEFORE" == "staged" ]] || fail "暂存后 update_service 记录状态=$PUB_STATE_BEFORE"
      pass "升级包已暂存（update_service=staged）"

      PREV_SESSION=$SESSION_ID
      curl -fsS -X POST -H "$AUTH" -H "$CT" \
        -d '{"reason":"smoke agent approval","ttl_seconds":600}' \
        "$ADMIN/admin/releases/v2/runs/$RUN_ID/approvals" >/dev/null || fail "创建审批失败"
      pass "已批准（默认触发自动续跑）"

      RESULT=$(agent_wait_finished "$PREV_SESSION")
      SESSION_ID=${RESULT%% *}
      SESSION_STATUS=${RESULT##* }
      [[ "$SESSION_STATUS" == "finished" ]] || fail "续跑会话状态=$SESSION_STATUS"
      pass "续跑会话结束：finished（$SESSION_ID）"

      DEADLINE=$((SECONDS + TIMEOUT))
      PUB=""
      while (( SECONDS < DEADLINE )); do
        PUB=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID")
        STATUS=$(echo "$PUB" | json_field 'd["data"].get("run",d["data"])["status"]')
        [[ "$STATUS" == "published" ]] && break
        sleep 2
      done
      assert_published "$PUB"
      DETAIL=$PUB
      ;;
  esac
fi

if [[ "$MODE" == "fail" ]]; then
  DEADLINE=$((SECONDS + TIMEOUT))
  STATUS=""
  while (( SECONDS < DEADLINE )); do
    DETAIL=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID")
    STATUS=$(echo "$DETAIL" | json_field 'd["data"].get("run",d["data"])["status"]')
    case "$STATUS" in
      failed) break ;;
      built|published) fail "预期失败，但运行到了 $STATUS" ;;
    esac
    sleep 2
  done
  [[ "$STATUS" == "failed" ]] || fail "等待 $TIMEOUT 秒运行仍未 failed（当前 $STATUS）"
  pass "运行状态：failed（构建按预期失败）"

  ERR_CODE=$(echo "$DETAIL" | json_field '(d["data"].get("run") or d["data"]).get("error_code") or ""')
  ERR_MSG=$(echo "$DETAIL" | json_field '(d["data"].get("run") or d["data"]).get("error_message") or ""')
  [[ -n "$ERR_MSG" ]] || fail "失败运行没有 error_message"
  pass "错误可见：${ERR_CODE:-no-code} / $(echo "$ERR_MSG" | tr '\n' ' ' | head -c 120)"

  ARTS=$(echo "$DETAIL" | json_field 'len(d["data"].get("artifacts") or [])')
  PUBS=$(echo "$DETAIL" | json_field 'len(d["data"].get("publications") or [])')
  [[ "$ARTS" == "0" ]] || fail "失败运行不应登记制品（实际 $ARTS 个）"
  [[ "$PUBS" == "0" ]] || fail "失败运行不应产生发布记录（实际 $PUBS 个）"
  pass "无制品、无发布记录（渠道未被推进）"
fi

TARGET_STATUS=built
[[ "$MODE" == "stage" || "$MODE" == "publish" ]] && TARGET_STATUS=verifying
DEADLINE=$((SECONDS + TIMEOUT))
STATUS=""
while [[ $MODE_AGENT -eq 0 && "$MODE" != "fail" ]] && (( SECONDS < DEADLINE )); do
  DETAIL=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID")
  STATUS=$(echo "$DETAIL" | json_field 'd["data"].get("run",d["data"])["status"]')
  [[ "$STATUS" == "$TARGET_STATUS" ]] && break
  case "$STATUS" in
    failed|blocked|cancelled)
      echo "$DETAIL" | python3 -m json.tool | head -40 >&2 || true
      fail "运行停在 $STATUS"
      ;;
  esac
  sleep 2
done
if [[ $MODE_AGENT -eq 0 && "$MODE" != "fail" ]]; then
  [[ "$STATUS" == "$TARGET_STATUS" ]] || fail "等待 $TIMEOUT 秒仍未 $TARGET_STATUS（当前 $STATUS）"
  pass "运行状态：$STATUS"
fi

if [[ "$MODE" != "fail" ]]; then
DETAIL_FILE=$(mktemp)
echo "$DETAIL" > "$DETAIL_FILE"
python3 - "$DETAIL_FILE" <<'PY' || { rm -f "$DETAIL_FILE"; exit 1; }
import json, sys
d = json.load(open(sys.argv[1]))["data"]
run = d.get("run", d)
targets = d.get("targets") or run.get("targets") or []
bad = [t.get("id") for t in targets if t.get("status") != "ready"]
arts = d.get("artifacts") or run.get("artifacts") or []
if bad:
    print("FAIL: 目标未 ready: %s" % bad, file=sys.stderr)
    sys.exit(1)
if not arts:
    print("FAIL: 没有登记制品", file=sys.stderr)
    sys.exit(1)
missing = [a.get("name") for a in arts if not str(a.get("sha256") or "").startswith("sha256:")]
if missing:
    print("FAIL: 制品缺少摘要: %s" % missing, file=sys.stderr)
    sys.exit(1)
print("PASS: 目标 %d 个 ready，制品 %d 个" % (len(targets), len(arts)))
for a in arts:
    print("  - %s %s %s" % (a.get("role"), a.get("sha256"), a.get("storage_key")))
PY
rm -f "$DETAIL_FILE"
fi

if [[ "$MODE" == "stage" || "$MODE" == "publish" ]]; then
  SCOPES='"release.read","artifact.read","artifact.write","update.read","update.write","update.publish"'
  [[ "$MODE" == "publish" ]] && SCOPES="$SCOPES,\"github.read\",\"github.write\",\"release.publish\""
  TOKEN=$(curl -fsS -X POST -H "$AUTH" -H "$CT" \
    -d "{\"scopes\":[$SCOPES],\"ttl_seconds\":900}" \
    "$ADMIN/admin/releases/v2/runs/$RUN_ID/agent-token" | json_field 'd["data"]["token"]')
  [[ "$TOKEN" == rmt_* ]] || fail "签发 Agent token 失败"
  pass "已签发运行级 token"

  mcp_call() {
    local name=$1
    local args=${2:-"{}"}
    local body out
    body=$(python3 -c "import json,sys;print(json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':sys.argv[1],'arguments':json.loads(sys.argv[2])}}))" "$name" "$args")
    out=$(mktemp)
    curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "$CT" -d "$body" "$MCP" > "$out" || { rm -f "$out"; fail "MCP $name 请求失败"; }
    python3 - "$out" "$name" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
res = d.get("result") or {}
if d.get("error"):
    print("FAIL: %s rpc error %s" % (sys.argv[2], d["error"]), file=sys.stderr)
    sys.exit(1)
code = (res.get("structuredContent") or {}).get("code", "")
print("%s -> isError=%s code=%s" % (sys.argv[2], res.get("isError"), code or "-"))
print("::%s::%s::%s" % (sys.argv[2], "true" if res.get("isError") else "false", code))
PY
    rm -f "$out"
  }

  expect_ok() {
    local name=$1 args=${2:-"{}"} out
    out=$(mcp_call "$name" "$args")
    echo "$out" | grep -v '^::'
    echo "$out" | grep -q "::$name::false::" || fail "$name 失败"
  }

  expect_ok update_bundle_build
  expect_ok update_import_stage
  pass "升级包已暂存"

  if [[ "$MODE" == "publish" ]]; then
    expect_ok github_draft_create
    pass "GitHub 草稿已创建"
    expect_ok github_asset_upload "{\"role\":\"$ROLE\"}"
    pass "GitHub 附件已上传（$ROLE）"
    FINAL_TOOL=release_publish
  else
    FINAL_TOOL=update_channel_promote
  fi

  GATE=$(mcp_call "$FINAL_TOOL" "{\"channel\":\"$CHANNEL\"}")
  echo "$GATE" | grep -v '^::'
  echo "$GATE" | grep -q "::$FINAL_TOOL::true::approval_required" || fail "审批门禁未生效"
  pass "审批门禁生效（approval_required）"

  curl -fsS -X POST -H "$AUTH" -H "$CT" \
    -d '{"reason":"smoke auto approval","ttl_seconds":600,"auto_resume":false}' \
    "$ADMIN/admin/releases/v2/runs/$RUN_ID/approvals" >/dev/null || fail "创建审批失败"
  pass "已批准（内容摘要绑定，未触发自动续跑）"

  expect_ok "$FINAL_TOOL" "{\"channel\":\"$CHANNEL\"}"
  pass "渠道已推进"

  DEADLINE=$((SECONDS + TIMEOUT))
  while (( SECONDS < DEADLINE )); do
    PUB=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID")
    STATUS=$(echo "$PUB" | json_field 'd["data"].get("run",d["data"])["status"]')
    [[ "$STATUS" == "published" ]] && break
    sleep 2
  done
  [[ "$STATUS" == "published" ]] || fail "推进后未 published（当前 $STATUS）"

  PUB_STATE=$(echo "$PUB" | json_field 'next((p.get("state") for p in (d["data"].get("publications") or []) if p.get("provider")=="update_service"), "")')
  [[ "$PUB_STATE" == "promoted" ]] || fail "update_service 发布记录状态=$PUB_STATE"
  pass "运行已发布（update_service=promoted）"

  if [[ "$MODE" == "publish" ]]; then
    GH_ID=$(echo "$PUB" | json_field 'next((p.get("external_id") for p in (d["data"].get("publications") or []) if p.get("provider")=="github"), "")')
    [[ -n "$GH_ID" ]] || fail "缺少 GitHub 发布记录"
    GH_STATE=$(echo "$PUB" | json_field 'next((p.get("state") for p in (d["data"].get("publications") or []) if p.get("provider")=="github"), "")')
    [[ "$GH_STATE" == "published" ]] || fail "GitHub 发布记录状态=$GH_STATE"
    GH_DETAIL=$(curl -fsS -H "Authorization: Bearer smoke" "$GH_BASE/repos/$GH_REPO/releases/$GH_ID")
    python3 - "$GH_DETAIL" <<'PY' || fail "假 GitHub 回读不一致"
import json, sys
rel = json.loads(sys.argv[1])
assets = rel.get("assets") or []
if not rel.get("published"):
    print("FAIL: github release published=%s" % rel.get("published"), file=sys.stderr)
    sys.exit(1)
if not assets:
    print("FAIL: github release 没有附件", file=sys.stderr)
    sys.exit(1)
print("PASS: GitHub release %s published，附件 %d 个" % (rel.get("id"), len(assets)))
for a in assets:
    print("  - %s %s bytes" % (a.get("name"), a.get("size")))
PY
    pass "GitHub 回读一致（published + 附件）"
  fi

  AGENT_STATE=$(curl -fsS -H "$AUTH" "$ADMIN/admin/releases/v2/runs/$RUN_ID/agent" | json_field 'd["data"].get("running")')
  [[ "$AGENT_STATE" == "False" ]] || fail "手动推进不应拉起发布 Agent（running=$AGENT_STATE）"
  pass "未触发自动续跑会话"
fi

echo "SMOKE_OK run=$RUN_ID project=$PROJECT_ID version=$VERSION mode=$MODE"