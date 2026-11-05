#!/usr/bin/env bash
# Linux 运行时冒烟：用 Linux 编译出的 admin-api 连真实 Postgres 起进程，
# 验证能迁移/启动、/healthz 200、带管理令牌能读项目列表，然后退出。
# 用法：SMARTPIPE_BIN=/tmp/smartpipe-bin ./scripts/linux-runtime-smoke.sh
set -euo pipefail
BIN="${SMARTPIPE_BIN:-/tmp/smartpipe-bin}"
DSN="${SMARTPIPE_DSN:-postgres://smartpipe:smartpipe_dev_2026@192.168.1.99:5432/smartpipe?sslmode=disable}"
PORT="${SMARTPIPE_SMOKE_PORT:-18092}"
TOKEN="linux-smoke-token"
KEY="$(head -c 32 /dev/urandom | base64)"
LOG="/tmp/smartpipe-linux-admin.log"

cleanup() {
  if [ -n "${PID:-}" ] && kill -0 "$PID" 2>/dev/null; then kill "$PID" 2>/dev/null || true; fi
}
trap cleanup EXIT

SMARTPIPE_DSN="$DSN" \
SMARTPIPE_MASTER_KEY="$KEY" \
SMARTPIPE_ADMIN_TOKEN="$TOKEN" \
SMARTPIPE_ADMIN_ADDR=":$PORT" \
  "$BIN/admin-api" > "$LOG" 2>&1 &
PID=$!

health=""
for i in $(seq 1 20); do
  sleep 0.5
  health="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/healthz" || true)"
  [ "$health" = "200" ] && break
done
if [ "$health" != "200" ]; then
  echo "healthz failed: $health"
  tail -20 "$LOG"
  exit 1
fi
echo "healthz=200 pid=$PID"

code="$(curl -s -o /tmp/smartpipe-projects.json -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT/admin/projects")"
echo "projects=$code"
if [ "$code" != "200" ]; then tail -20 "$LOG"; exit 1; fi
python3 - <<'PY'
import json
d = json.load(open("/tmp/smartpipe-projects.json"))
items = d.get("data") or []
print("projects_count=%d first=%s" % (len(items), items[0]["name"] if items else "-"))
PY
grep -c "migrat" "$LOG" >/dev/null 2>&1 && echo "migrations_logged=yes" || echo "migrations_logged=no"
echo "LINUX RUNTIME SMOKE OK"