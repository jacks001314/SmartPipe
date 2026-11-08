#!/usr/bin/env bash
# Linux 侧 SIT：与 Windows 的 scripts/sit-all.ps1 对应，覆盖 build + vet + test。
# 用法：SMARTPIPE_TEST_DSN='postgres://...' ./scripts/sit-linux.sh [--skip-test]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SKIP_TEST=0
for a in "$@"; do
  case "$a" in
    --skip-test) SKIP_TEST=1 ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
BIN="${SMARTPIPE_BIN:-/tmp/smartpipe-bin}"
mkdir -p "$BIN"
cd "$ROOT/backend"
echo "== env =="
go version
git --version 2>/dev/null || true
echo "== build =="
for c in admin-api bridge patch-verifier bot; do
  go build -trimpath -o "$BIN/$c" "./cmd/$c"
  printf 'built %-16s %s bytes\n' "$c" "$(stat -c%s "$BIN/$c")"
done
echo "== vet =="
go vet ./...
echo "== test =="
if [ "$SKIP_TEST" = "1" ]; then
  echo "skipped by --skip-test"
else
  : "${SMARTPIPE_TEST_DSN:?需要 SMARTPIPE_TEST_DSN 指向测试库（smartpipe_test）}"
  go test -p 1 -count=1 ./...
fi
echo "LINUX SIT OK"