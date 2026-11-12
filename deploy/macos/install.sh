#!/bin/bash
# 在 macOS 构建机上安装 release-executor，并注册为 launchd 常驻服务。
#
# 用法：
#   curl -fsSL <站内地址>/release-executor-darwin-arm64 -o /tmp/release-executor
#   sudo bash install.sh /tmp/release-executor
#   bash install.sh --dry-run /tmp/release-executor   # 只渲染并校验 plist，不改系统
#
# 需要提前准备（也可以用环境变量传进来）：
#   SMARTPIPE_RELEASE_SERVICE_URL     release-service 地址，例如 http://10.0.0.5:18099
#   SMARTPIPE_RELEASE_SERVICE_TOKEN   执行器令牌（只存在于本机，不进仓库）
#   SMARTPIPE_RELEASE_EXECUTOR_ID     稳定执行器 id，例如 mac-mini-01
#
# 令牌写进 /usr/local/etc/smartpipe/release-executor.env（0600 root），
# plist 只调用包装脚本，避免 `launchctl print` 之类的命令把令牌打出来。
set -euo pipefail

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  shift
fi

BIN_SRC="${1:-}"
if [[ -z "${BIN_SRC}" || ! -f "${BIN_SRC}" ]]; then
  echo "usage: bash install.sh [--dry-run] /path/to/release-executor" >&2
  exit 2
fi

SERVICE_URL="${SMARTPIPE_RELEASE_SERVICE_URL:-}"
SERVICE_TOKEN="${SMARTPIPE_RELEASE_SERVICE_TOKEN:-}"
EXECUTOR_ID="${SMARTPIPE_RELEASE_EXECUTOR_ID:-$(hostname -s | tr 'A-Z' 'a-z')}"
if [[ -z "${SERVICE_URL}" ]]; then
  if [[ "$DRY_RUN" == "1" ]]; then SERVICE_URL="http://<release-service>:18099"; else read -r -p "release-service URL: " SERVICE_URL; fi
fi
if [[ -z "${SERVICE_TOKEN}" ]]; then
  if [[ "$DRY_RUN" == "1" ]]; then SERVICE_TOKEN="<token>"; else
    read -r -s -p "executor token: " SERVICE_TOKEN
    echo
  fi
fi

PREFIX=/usr/local
PLIST=/Library/LaunchDaemons/com.smartpipe.release-executor.plist
ENV_DIR="${PREFIX}/etc/smartpipe"
ENV_FILE="${ENV_DIR}/release-executor.env"
RUNNER="${PREFIX}/bin/smartpipe-release-executor-runner"
LOG_DIR="${PREFIX}/var/log"
HERE="$(cd "$(dirname "$0")" && pwd)"

render_plist() {
  sed \
    -e "s|__SERVICE_URL__|${SERVICE_URL}|g" \
    -e "s|__SERVICE_TOKEN__|${SERVICE_TOKEN}|g" \
    -e "s|__EXECUTOR_ID__|${EXECUTOR_ID}|g" \
    "$HERE/com.smartpipe.release-executor.plist"
}

validate_plist() {
  local file=$1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$file" <<'PY'
import plistlib, sys
with open(sys.argv[1], "rb") as fh:
    data = plistlib.load(fh)
assert data.get("Label") == "com.smartpipe.release-executor", data
assert data.get("RunAtLoad") is True and data.get("KeepAlive") is True, data
args = data.get("ProgramArguments") or []
assert args, "ProgramArguments 为空"
print("plist OK:", args[0], "| log:", data.get("StandardOutPath"))
PY
  else
    echo "plist 校验跳过：没有 python3" >&2
  fi
}

if [[ "$DRY_RUN" == "1" ]]; then
  echo "[dry-run] 二进制：$BIN_SRC"
  echo "[dry-run] 安装到：${PREFIX}/bin/release-executor"
  echo "[dry-run] 环境文件：$ENV_FILE（0600 root:wheel）"
  echo "[dry-run] 包装脚本：$RUNNER"
  echo "[dry-run] launchd：$PLIST"
  echo "[dry-run] executor id：$EXECUTOR_ID"
  tmp=$(mktemp)
  render_plist > "$tmp"
  validate_plist "$tmp"
  sed -n '1,12p' "$tmp"
  rm -f "$tmp"
  echo "[dry-run] 结束，未修改系统。"
  exit 0
fi

install -d -m 755 "${PREFIX}/bin" "${LOG_DIR}"
install -d -m 700 "$ENV_DIR"
install -m 755 "${BIN_SRC}" "${PREFIX}/bin/release-executor"

cat > "$RUNNER" <<'RUNNER'
#!/bin/sh
# 由 launchd 调用：加载 0600 环境文件后 exec 执行器。
set -a
. /usr/local/etc/smartpipe/release-executor.env
set +a
exec /usr/local/bin/release-executor
RUNNER
chmod 755 "$RUNNER"
chown root:wheel "$RUNNER"

umask 077
cat > "$ENV_FILE" <<ENV
SMARTPIPE_RELEASE_SERVICE_URL=${SERVICE_URL}
SMARTPIPE_RELEASE_SERVICE_TOKEN=${SERVICE_TOKEN}
SMARTPIPE_RELEASE_EXECUTOR_ID=${EXECUTOR_ID}
ENV
chmod 600 "$ENV_FILE"
chown root:wheel "$ENV_FILE"

render_plist > "${PLIST}.tmp"
validate_plist "${PLIST}.tmp"
mv "${PLIST}.tmp" "${PLIST}"
chmod 644 "${PLIST}"
chown root:wheel "${PLIST}"

launchctl bootout system "${PLIST}" >/dev/null 2>&1 || true
launchctl bootstrap system "${PLIST}"
launchctl enable system/com.smartpipe.release-executor

echo "已安装并启动：launchctl print system/com.smartpipe.release-executor"
echo "日志：${LOG_DIR}/smartpipe-release-executor.log"
echo "环境文件：${ENV_FILE}（改完执行 launchctl kickstart -k system/com.smartpipe.release-executor）"