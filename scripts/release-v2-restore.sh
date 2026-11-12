#!/bin/bash
# release-v2-restore.sh：把 release-v2-backup.sh 的产物恢复到一个目标库并核对行数。
#
# 用法：
#   sudo bash scripts/release-v2-restore.sh <备份目录> <目标库> [--force]
#
# 默认拒绝覆盖生产库名（SMARTPIPE_DB_NAME，默认 smartpipe_test），确需覆盖时加 --force。
# 建议先恢复到临时库演练，确认无误后再决定是否切换。
set -euo pipefail

DIR=${1:?用法: release-v2-restore.sh <备份目录> <目标库> [--force]}
DST=${2:?缺少目标库名}
FORCE=${3:-}
CONTAINER=${SMARTPIPE_DB_CONTAINER:-smartpipe-postgres}
USER=${SMARTPIPE_DB_USER:-smartpipe}
LIVE=${SMARTPIPE_DB_NAME:-smartpipe_test}
DUMP="$DIR/smartpipe.dump"

fail() { echo "FAIL: $*" >&2; exit 1; }
[[ -s "$DUMP" ]] || fail "找不到备份文件 $DUMP"
if [[ "$DST" == "$LIVE" && "$FORCE" != "--force" ]]; then
  fail "拒绝覆盖 $LIVE（这是当前在线库）；确认要覆盖时加 --force"
fi

q() { docker exec "$CONTAINER" psql -U "$USER" -d "$1" -tAc "$2"; }

echo "从 $DUMP 恢复到 $DST（容器 $CONTAINER）"
docker exec "$CONTAINER" psql -U "$USER" -d postgres -tAc "select 1 from pg_database where datname='$DST'" | grep -q 1 \
  && docker exec "$CONTAINER" dropdb -U "$USER" --force "$DST"
docker exec "$CONTAINER" createdb -U "$USER" "$DST"
docker exec -i "$CONTAINER" pg_restore -U "$USER" -d "$DST" --no-owner --no-privileges < "$DUMP"

echo "migrations=$(q "$DST" 'select max(version) from schema_migrations')"
for t in release_v2_runs release_v2_artifacts release_v2_agent_sessions; do
  echo "$t=$(q "$DST" "select count(*) from $t")"
done

if [[ -r "$DIR/manifest.txt" ]]; then
  echo "--- 期望值（来自备份 manifest）---"
  grep -E '^(migrations|runs|artifacts|sessions)=' "$DIR/manifest.txt" || true
  want_runs=$(sed -n 's/^runs=//p' "$DIR/manifest.txt")
  got_runs=$(q "$DST" 'select count(*) from release_v2_runs')
  [[ -z "$want_runs" || "$want_runs" == "$got_runs" ]] || fail "运行行数不一致：备份 $want_runs，恢复 $got_runs"
fi

echo "RESTORE_OK $DST"