#!/usr/bin/env bash
# Install or in-place update a Linux host from this checkout.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="docker"
INSTALL_DIR="/opt/smartpipe"
INSTALL_ARGS=()

usage() {
  cat <<'EOF'
Usage: scripts/deploy.sh [options] [install.sh options]
  --mode docker|binary   Deployment mode (default docker)
  --install-dir DIR      Install directory (default /opt/smartpipe)
  --no-build             Reuse the existing image or binary bundle
  --bundle DIR           Binary bundle produced by build.sh
  --env-file FILE        Environment file to install
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="${2:?missing mode}"; shift 2 ;;
    --install-dir) INSTALL_DIR="${2:?missing install directory}"; shift 2 ;;
    --no-build) INSTALL_ARGS+=(--skip-build); shift ;;
    --bundle) INSTALL_ARGS+=(--bundle "${2:?missing bundle}"); shift 2 ;;
    --env-file) INSTALL_ARGS+=(--env-file "${2:?missing env file}"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  exec sudo -- "$0" --mode "$MODE" --install-dir "$INSTALL_DIR" "${INSTALL_ARGS[@]}" "$@"
fi

exec "$ROOT/scripts/install.sh" --mode "$MODE" --install-dir "$INSTALL_DIR" "${INSTALL_ARGS[@]}"
