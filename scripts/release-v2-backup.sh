#!/bin/bash
# release-v2-backup.sh：备份 release v2 的 Postgres 库，并记录制品清单摘要。
#
# 用法：
#   sudo bash scripts/release-v2-backup.sh [输出目录]
# 默认输出到 /var/backups/smartpipe/release-v2/<时间戳>。
#
# 环境变量：
#   SMARTPIPE_DB_CONTAINER   默认 smartpipe-postgres
#   SMARTPIPE_DB_NAME        默认 smartpipe_test
#   SMARTPIPE_DB_USER        默认 smartpipe
#   SMARTPIPE_MINIO_CONTAINER 默认 geometry-minio（容器里有 mc 时额外导出对象清单）
#   SMARTPIPE_S3_BUCKET      默认 smartpipe-releases（配合 SMARTPIPE_S3_* 访问 MinIO）
set -euo pipefail

OUT=${1:-/var/backups/smartpipe/release-v2/$(date +%Y%m%d-%H%M%S)}
CONTAINER=${SMARTPIPE_DB_CONTAINER:-smartpipe-postgres}
DB=${SMARTPIPE_DB_NAME:-smartpipe_test}
USER=${SMARTPIPE_DB_USER:-smartpipe}
MINIO=${SMARTPIPE_MINIO_CONTAINER:-geometry-minio}
BUCKET=${SMARTPIPE_S3_BUCKET:-smartpipe-releases}

fail() { echo "FAIL: $*" >&2; exit 1; }
mkdir -p "$OUT"

command -v docker >/dev/null || fail "找不到 docker"
docker inspect "$CONTAINER" >/dev/null 2>&1 || fail "容器 $CONTAINER 不存在"

echo "备份库 $DB（容器 $CONTAINER）到 $OUT"
docker exec "$CONTAINER" pg_dump -U "$USER" -d "$DB" -Fc > "$OUT/smartpipe.dump"
[[ -s "$OUT/smartpipe.dump" ]] || fail "pg_dump 产物为空"

MANIFEST="$OUT/manifest.txt"
{
  echo "time=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "database=$DB"
  echo "migrations=$(docker exec "$CONTAINER" psql -U "$USER" -d "$DB" -tAc 'select max(version) from schema_migrations')"
  echo "runs=$(docker exec "$CONTAINER" psql -U "$USER" -d "$DB" -tAc 'select count(*) from release_v2_runs')"
  echo "artifacts=$(docker exec "$CONTAINER" psql -U "$USER" -d "$DB" -tAc 'select count(*) from release_v2_artifacts')"
  echo "sessions=$(docker exec "$CONTAINER" psql -U "$USER" -d "$DB" -tAc 'select count(*) from release_v2_agent_sessions')"
  echo "dump_sha256=$(sha256sum "$OUT/smartpipe.dump" | awk '{print $1}')"
  echo "dump_bytes=$(stat -c %s "$OUT/smartpipe.dump")"
} > "$MANIFEST"

# 制品对象清单：容器内有 mc 且能连上 MinIO 时导出 key/size，便于核对桶内对象是否齐全。
if docker exec "$MINIO" sh -c 'command -v mc' >/dev/null 2>&1; then
  AK=${SMARTPIPE_S3_ACCESS_KEY:-}
  SK=${SMARTPIPE_S3_SECRET_KEY:-}
  if [[ -n "$AK" && -n "$SK" ]]; then
    if docker exec "$MINIO" sh -c "mc alias set smartpipe-backup http://127.0.0.1:9000 '$AK' '$SK' >/dev/null && mc ls --recursive smartpipe-backup/$BUCKET" > "$OUT/objects.txt" 2>/dev/null; then
      echo "objects=$(wc -l < "$OUT/objects.txt")" >> "$MANIFEST"
    else
      echo "objects=unavailable" >> "$MANIFEST"
    fi
  else
    echo "objects=skipped（缺少 SMARTPIPE_S3_ACCESS_KEY/SMARTPIPE_S3_SECRET_KEY）" >> "$MANIFEST"
  fi
else
  echo "objects=skipped（容器内没有 mc）" >> "$MANIFEST"
fi

cat "$MANIFEST"
echo "BACKUP_OK $OUT"