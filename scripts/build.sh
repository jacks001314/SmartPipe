#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="0.1.0"
OUTPUT="dist"
TARGET_OS="$(go env GOOS 2>/dev/null || echo linux)"
ARCH="$(go env GOARCH 2>/dev/null || echo amd64)"
SKIP_WEB=0
NO_RESTORE=0
SKIP_ARCHIVE=0

usage() {
  cat <<'EOF'
Usage: scripts/build.sh [options]
  --version VERSION    Version injected into admin-api and the manifest (default 0.1.0)
  --output DIR         Output directory inside the repository (default dist)
  --os OS              Target GOOS (default current host)
  --arch ARCH          Target GOARCH (default current host)
  --skip-web           Reuse the existing web/dist
  --no-restore         Skip npm ci
  --skip-archive       Do not create a tar.gz bundle
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:?missing version}"; shift 2 ;;
    --output) OUTPUT="${2:?missing output}"; shift 2 ;;
    --os) TARGET_OS="${2:?missing os}"; shift 2 ;;
    --arch) ARCH="${2:?missing arch}"; shift 2 ;;
    --skip-web) SKIP_WEB=1; shift ;;
    --no-restore) NO_RESTORE=1; shift ;;
    --skip-archive) SKIP_ARCHIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$OUTPUT" in
  /*) OUT="$OUTPUT" ;;
  *) OUT="$ROOT/$OUTPUT" ;;
esac
OUT="$(realpath -m "$OUT")"
ROOT_REAL="$(realpath -m "$ROOT")"
if [[ "$OUT" != "$ROOT_REAL"/* ]]; then
  echo "output directory must stay inside the repository: $OUT" >&2
  exit 2
fi
if [[ "$OUT" == "$ROOT_REAL" ]]; then
  echo "output directory cannot be the repository root" >&2
  exit 2
fi

COMMIT="$(git -C "$ROOT" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
DIRTY=false
if [[ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null | head -1)" ]]; then DIRTY=true; fi
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

rm -rf -- "$OUT"
mkdir -p "$OUT/bin"

export CGO_ENABLED=0
export GOOS="$TARGET_OS"
export GOARCH="$ARCH"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

if [[ "$SKIP_WEB" -eq 0 ]]; then
  (
    cd "$ROOT/web"
    if [[ "$NO_RESTORE" -eq 0 ]]; then npm ci --no-audit --no-fund; fi
    npm run build
  )
fi
if [[ ! -f "$ROOT/web/dist/index.html" ]]; then
  echo "web/dist/index.html is missing; run without --skip-web first" >&2
  exit 1
fi
cp -a "$ROOT/web/dist" "$OUT/web"

EXT=""
if [[ "$TARGET_OS" == "windows" ]]; then EXT=".exe"; fi
LDFLAGS="-s -w -X main.version=$VERSION"
(
  cd "$ROOT/backend"
  for pair in \
    "admin-api ./cmd/admin-api" \
    "bridge ./cmd/bridge" \
    "patch-verifier ./cmd/patch-verifier" \
    "mcp-gateway ./cmd/mcp-gateway" \
    "bot ./cmd/bot" \
    "botdiag ./cmd/botdiag"; do
    set -- $pair
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/bin/$1$EXT" "$2"
  done
)

if [[ -d "$ROOT/configs" ]]; then cp -a "$ROOT/configs" "$OUT/configs"; fi
if [[ -f "$ROOT/smartpipe.yaml" ]]; then cp -a "$ROOT/smartpipe.yaml" "$OUT/smartpipe.yaml"; fi

ARTIFACTS=""
for file in "$OUT"/bin/*; do
  name="$(basename "$file")"
  size="$(stat -c %s "$file")"
  hash="$(sha256sum "$file" | awk '{print $1}')"
  if [[ -n "$ARTIFACTS" ]]; then ARTIFACTS="$ARTIFACTS,"; fi
  ARTIFACTS="${ARTIFACTS}{\"name\":\"$name\",\"bytes\":$size,\"sha256\":\"$hash\"}"
done
cat > "$OUT/manifest.json" <<EOF
{
  "name": "SmartPipe",
  "version": "$VERSION",
  "commit": "$COMMIT",
  "dirty": $DIRTY,
  "built_at": "$BUILT_AT",
  "goos": "$TARGET_OS",
  "goarch": "$ARCH",
  "artifacts": [$ARTIFACTS]
}
EOF

(
  cd "$OUT"
  find . -type f ! -name SHA256SUMS -printf '%P\0' | sort -z | while IFS= read -r -d '' file; do
    sha256sum "$file"
  done > SHA256SUMS
)

if [[ "$SKIP_ARCHIVE" -eq 0 ]]; then
  ARCHIVE="$OUT/smartpipe-$VERSION-$TARGET_OS-$ARCH.tar.gz"
  ARCHIVE_PATHS=(bin web smartpipe.yaml manifest.json SHA256SUMS)
  if [[ -d "$OUT/configs" ]]; then ARCHIVE_PATHS+=(configs); fi
  tar -czf "$ARCHIVE" -C "$OUT" "${ARCHIVE_PATHS[@]}"
  echo "ARCHIVE $ARCHIVE"
fi

echo "BUILD OK version=$VERSION commit=$COMMIT os=$TARGET_OS/$ARCH"
echo "OUTPUT $OUT"
