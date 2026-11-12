#!/bin/bash
# install-prod.sh：在这台机器上安装一套正式档 SmartPipe 发版系统。
#
# 和测试档（/opt/smartpipe-v2-test、1809x 端口、fixture、update-stub、假 GitHub）完全分开：
#   - 目录 /opt/smartpipe，状态 /var/lib/smartpipe，配置 /etc/smartpipe/prod
#   - 端口 28092(admin) / 28099(release) / 28097(mcp)
#   - 数据库 smartpipe（独立库，独立角色）
#   - 单元 smartpipe-admin / smartpipe-release / smartpipe-mcp / smartpipe-executor
#
# 用法（在已把四个二进制和前端目录放到 --src 下之后）：
#   sudo bash install-prod.sh --src /tmp/smartpipe-prod-src
#
# --src 里需要：admin-api、release-service、release-mcp、release-executor、web/index.html
# 生成的密码与令牌只写进 /etc/smartpipe/prod/*.env（0600 root），脚本本身不打印。
set -euo pipefail

SRC=/tmp/smartpipe-prod-src
PREFIX=/opt/smartpipe
STATE=/var/lib/smartpipe
CONF=/etc/smartpipe/prod
DB_NAME=smartpipe_release
DB_USER=smartpipe_rel
BIN_VERSION_FILE=$PREFIX/VERSION

while [[ $# -gt 0 ]]; do
  case "$1" in
    --src) SRC=$2; shift 2 ;;
    --prefix) PREFIX=$2; shift 2 ;;
    *) echo "未知参数 $1" >&2; exit 2 ;;
  esac
done

[[ $EUID -eq 0 ]] || { echo "需要 root" >&2; exit 1; }
for f in admin-api release-service release-mcp release-executor; do
  [[ -f "$SRC/$f" ]] || { echo "缺少 $SRC/$f" >&2; exit 1; }
done
[[ -f "$SRC/web/index.html" ]] || { echo "缺少 $SRC/web/index.html" >&2; exit 1; }

say() { echo "[prod] $*"; }

# 1. 用户与目录
if ! id smartpipe >/dev/null 2>&1; then
  useradd --system --home "$STATE" --shell /usr/sbin/nologin smartpipe
  say "已创建系统用户 smartpipe"
fi
install -d -o smartpipe -g smartpipe -m 755 "$PREFIX" "$PREFIX/bin" "$PREFIX/logs"
install -d -o smartpipe -g smartpipe -m 755 "$STATE" "$STATE/artifacts" "$STATE/agent-workspaces" "$STATE/codex-home"
install -d -o root -g smartpipe -m 750 "$CONF"
install -d -o smartpipe -g smartpipe -m 755 "$PREFIX/web"

install -o smartpipe -g smartpipe -m 755 "$SRC/admin-api" "$PREFIX/bin/admin-api"
install -o smartpipe -g smartpipe -m 755 "$SRC/release-service" "$PREFIX/bin/release-service"
install -o smartpipe -g smartpipe -m 755 "$SRC/release-mcp" "$PREFIX/bin/release-mcp"
install -o smartpipe -g smartpipe -m 755 "$SRC/release-executor" "$PREFIX/bin/release-executor"
if [[ -d "$SRC/web" ]]; then
  rm -rf "$PREFIX/web.next"; cp -a "$SRC/web" "$PREFIX/web.next"
  chown -R smartpipe:smartpipe "$PREFIX/web.next"
  rm -rf "$PREFIX/web.old"; mv "$PREFIX/web" "$PREFIX/web.old"; mv "$PREFIX/web.next" "$PREFIX/web"
  rm -rf "$PREFIX/web.old"
fi
say "二进制与前端已安装到 $PREFIX"

# 2. 数据库（已存在 db.env 时直接复用，不重复生成密码）
# 管理通道：优先本地 socket 的 postgres 角色，其次 Docker 里的 smartpipe-postgres 容器。
PG_MODE=""
if sudo -u postgres psql -tAc "select 1" >/dev/null 2>&1; then
  PG_MODE=local
elif docker exec smartpipe-postgres psql -U smartpipe -tAc "select 1" >/dev/null 2>&1; then
  PG_MODE=docker
else
  echo "找不到可用的 PostgreSQL 管理通道（本地 socket 与 smartpipe-postgres 容器都不可用）" >&2
  exit 4
fi
say "数据库管理通道：$PG_MODE"

pg_sql() {
  if [[ "$PG_MODE" == local ]]; then
    sudo -u postgres psql -tAc "$1"
  else
    docker exec smartpipe-postgres psql -U smartpipe -tAc "$1"
  fi
}

pg_exec() {
  if [[ "$PG_MODE" == local ]]; then
    sudo -u postgres psql -c "$1" >/dev/null
  else
    docker exec smartpipe-postgres psql -U smartpipe -c "$1" >/dev/null
  fi
}

DB_PASS=""
DSN=""
if [[ -f "$CONF/db.env" ]]; then
  # shellcheck disable=SC1090
  . "$CONF/db.env"
  DSN=${SMARTPIPE_DSN:-}
  say "复用已有 $CONF/db.env"
fi
if [[ -z "$DSN" ]]; then
  if ! pg_sql "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'" | grep -q 1; then
    DB_PASS=$(openssl rand -hex 24)
    pg_exec "CREATE ROLE $DB_USER LOGIN PASSWORD '$DB_PASS'"
    say "已创建数据库角色 $DB_USER"
  else
    echo "数据库角色 $DB_USER 已存在，但脚本拿不到它的密码。" >&2
    echo "请把 SMARTPIPE_DSN 写进 $CONF/db.env 后重跑本脚本。" >&2
    exit 3
  fi
  if ! pg_sql "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" | grep -q 1; then
    pg_exec "CREATE DATABASE $DB_NAME OWNER $DB_USER"
    say "已创建数据库 $DB_NAME"
  fi
  DSN="postgres://$DB_USER:$DB_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable"
fi

# 3. 环境文件
MASTER_KEY_FILE=$CONF/master.key
if [[ ! -f "$MASTER_KEY_FILE" ]]; then
  if [[ -n "${SMARTPIPE_MASTER_KEY:-}" ]]; then
    printf '%s' "$SMARTPIPE_MASTER_KEY" > "$MASTER_KEY_FILE"
  else
    openssl rand -base64 32 > "$MASTER_KEY_FILE"
  fi
  chmod 600 "$MASTER_KEY_FILE"
fi
MASTER_KEY=$(cat "$MASTER_KEY_FILE")

gen() { openssl rand -hex 24; }
ADMIN_TOKEN_FILE=$CONF/admin.token
RELEASE_TOKEN_FILE=$CONF/release.token
if [[ ! -f "$ADMIN_TOKEN_FILE" ]]; then gen > "$ADMIN_TOKEN_FILE"; chmod 600 "$ADMIN_TOKEN_FILE"; fi
if [[ ! -f "$RELEASE_TOKEN_FILE" ]]; then gen > "$RELEASE_TOKEN_FILE"; chmod 600 "$RELEASE_TOKEN_FILE"; fi
ADMIN_TOKEN=$(cat "$ADMIN_TOKEN_FILE")
RELEASE_TOKEN=$(cat "$RELEASE_TOKEN_FILE")

ADMIN_PASSWORD_FILE=$CONF/admin.password
if [[ ! -f "$ADMIN_PASSWORD_FILE" ]]; then
  openssl rand -base64 18 | tr -d '/+=' | head -c 20 > "$ADMIN_PASSWORD_FILE"
  chmod 600 "$ADMIN_PASSWORD_FILE"
fi
ADMIN_PASSWORD=$(cat "$ADMIN_PASSWORD_FILE")

if [[ -z "$DB_PASS" ]]; then
  echo "数据库角色已存在但脚本拿不到它的密码。" >&2
  echo "请把 DSN 写进 $CONF/db.env（SMARTPIPE_DSN=postgres://...），再重跑本脚本。" >&2
  exit 3
fi
DSN="postgres://$DB_USER:$DB_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable"

write_env() {
  local path=$1; shift
  umask 077
  printf '%s\n' "$@" > "$path"
  chmod 600 "$path"
}

if [[ ! -f "$CONF/db.env" ]]; then
  write_env "$CONF/db.env" \
    "SMARTPIPE_DSN=$DSN" \
    "SMARTPIPE_MASTER_KEY=$MASTER_KEY"
fi

write_env "$CONF/release.env" \
  "SMARTPIPE_RELEASE_SERVICE_ADDR=:28099" \
  "SMARTPIPE_RELEASE_SERVICE_TOKEN=$RELEASE_TOKEN" \
  "SMARTPIPE_RELEASE_STORAGE_DIR=$STATE/artifacts" \
  "SMARTPIPE_RELEASE_MAX_ACTIVE_RUNS=2" \
  "SMARTPIPE_RELEASE_TARGET_MAX_ATTEMPTS=3"

write_env "$CONF/mcp.env" \
  "SMARTPIPE_RELEASE_MCP_ADDR=:28097" \
  "SMARTPIPE_RELEASE_STORAGE_DIR=$STATE/artifacts"

write_env "$CONF/executor.env" \
  "SMARTPIPE_RELEASE_SERVICE_URL=http://127.0.0.1:28099" \
  "SMARTPIPE_RELEASE_SERVICE_TOKEN=$RELEASE_TOKEN" \
  "SMARTPIPE_RELEASE_EXECUTOR_ID=$(hostname)-prod"

write_env "$CONF/admin.env" \
  "SMARTPIPE_ADMIN_ADDR=:28092" \
  "SMARTPIPE_ADMIN_TOKEN=$ADMIN_TOKEN" \
  "SMARTPIPE_ADMIN_USER=admin" \
  "SMARTPIPE_ADMIN_PASSWORD=$ADMIN_PASSWORD" \
  "SMARTPIPE_WEB_DIST=$PREFIX/web" \
  "SMARTPIPE_RELEASE_URL=http://127.0.0.1:28099" \
  "SMARTPIPE_RELEASE_TOKEN=$RELEASE_TOKEN" \
  "SMARTPIPE_RELEASE_MCP_URL=http://127.0.0.1:28097/mcp" \
  "SMARTPIPE_RELEASE_MCP_TOKEN=$RELEASE_TOKEN" \
  "SMARTPIPE_RELEASE_AGENT_WORK_ROOT=$STATE/agent-workspaces" \
  "SMARTPIPE_CODEX_HOME=$STATE/codex-home"

# 真实外部服务：运维填这两个文件，服务不因缺它们而启动失败。
[[ -f "$CONF/update.env" ]] || write_env "$CONF/update.env" \
  "# 真实升级服务（syntropy_update）" \
  "# SMARTPIPE_RELEASE_UPDATE_URL=https://update.example.com" \
  "# SMARTPIPE_RELEASE_UPDATE_CREDENTIAL=<credential-ref>" \
  "# SMARTPIPE_CRED_<credential-ref>=<token>"
[[ -f "$CONF/github.env" ]] || write_env "$CONF/github.env" \
  "# 真实 GitHub" \
  "# SMARTPIPE_GITHUB_API_BASE=https://api.github.com" \
  "# SMARTPIPE_RELEASE_GITHUB_CREDENTIAL=<credential-ref>" \
  "# SMARTPIPE_CRED_<credential-ref>=<github token>"
[[ -f "$CONF/s3.env" ]] || write_env "$CONF/s3.env" \
  "# 对象存储（MinIO/S3）；不填则制品落在 $STATE/artifacts" \
  "# SMARTPIPE_S3_ENDPOINT=https://s3.example.com" \
  "# SMARTPIPE_S3_BUCKET=smartpipe-releases" \
  "# SMARTPIPE_S3_ACCESS_KEY=..." \
  "# SMARTPIPE_S3_SECRET_KEY=..."

# 4. systemd 单元
write_unit() {
  local name=$1 exec=$2 envs=$3 extra=$4
  cat > "/etc/systemd/system/$name.service" <<UNIT
[Unit]
Description=SmartPipe $name
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=smartpipe
Group=smartpipe
WorkingDirectory=$STATE
EnvironmentFile=$CONF/db.env
EnvironmentFile=$envs
$extra
ExecStart=$PREFIX/bin/$exec
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
}

write_unit smartpipe-release release-service "$CONF/release.env" "EnvironmentFile=-$CONF/s3.env"
write_unit smartpipe-mcp release-mcp "$CONF/mcp.env" "EnvironmentFile=-$CONF/s3.env
EnvironmentFile=-$CONF/update.env
EnvironmentFile=-$CONF/github.env"
write_unit smartpipe-executor release-executor "$CONF/executor.env" ""
write_unit smartpipe-admin admin-api "$CONF/admin.env" "EnvironmentFile=-$CONF/update.env
EnvironmentFile=-$CONF/github.env"

systemctl daemon-reload
systemctl enable --now smartpipe-release smartpipe-mcp smartpipe-executor smartpipe-admin
sleep 5
say "单元状态："
systemctl is-active smartpipe-release smartpipe-mcp smartpipe-executor smartpipe-admin || true
say "健康检查："
curl -s -m 5 http://127.0.0.1:28099/healthz || true; echo
curl -s -m 5 http://127.0.0.1:28092/healthz || true; echo
say "配置目录 $CONF（0600）：db.env release.env mcp.env executor.env admin.env；待填 update.env github.env s3.env"
say "控制台：http://$(hostname -I | awk '{print $1}'):28092/"