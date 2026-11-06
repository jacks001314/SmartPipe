#!/usr/bin/env bash
# Read-only deployment verification. Never prints secret values.
set -euo pipefail

MODE="docker"
INSTALL_DIR="/opt/smartpipe"
ENV_PATH=""
SECRETS_ONLY=0

usage() {
  cat <<'EOF'
Usage: sudo scripts/verify-deploy.sh [options]
  --mode docker|binary
  --install-dir DIR
  --env-file FILE
  --secrets-only          Validate the environment file without checking services
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="${2:?missing mode}"; shift 2 ;;
    --install-dir) INSTALL_DIR="${2:?missing install dir}"; shift 2 ;;
    --env-file) ENV_PATH="${2:?missing env file}"; shift 2 ;;
    --secrets-only) SECRETS_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$ENV_PATH" ]]; then
  if [[ "$MODE" == "docker" ]]; then
    ENV_PATH="$INSTALL_DIR/deploy/.env"
  else
    ENV_PATH="/etc/smartpipe/smartpipe.env"
  fi
fi
if [[ ! -f "$ENV_PATH" ]]; then
  echo "environment file missing: $ENV_PATH" >&2
  exit 1
fi

get_value() {
  local key="$1"
  local raw
  raw="$(sed -n "s/^${key}=//p" "$ENV_PATH" | tail -1)"
  raw="${raw#\'}"
  raw="${raw%\'}"
  printf '%s' "$raw"
}

fail=0
mode="$(stat -c '%a' "$ENV_PATH")"
if [[ "$mode" != "600" ]]; then
  echo "FAIL env permissions: $mode $ENV_PATH" >&2
  fail=1
else
  echo "ENV_PERMISSIONS 600"
fi
if grep -Eq 'REPLACE_WITH|CHANGE_ME' "$ENV_PATH"; then
  echo "FAIL env file still contains template placeholders" >&2
  fail=1
fi

master="$(get_value SMARTPIPE_MASTER_KEY)"
master_bytes="$(printf '%s' "$master" | base64 -d 2>/dev/null | wc -c | tr -d ' ' || true)"
if [[ "$master_bytes" != "32" ]]; then
  echo "FAIL master key decodes to $master_bytes bytes (expected 32)" >&2
  fail=1
else
  echo "MASTER_KEY 32-bytes"
fi

for key in SMARTPIPE_ADMIN_TOKEN SMARTPIPE_BRIDGE_TOKEN SMARTPIPE_VERIFY_TOKEN \
  SMARTPIPE_RELEASE_TOKEN SMARTPIPE_BOT_INTERNAL_TOKEN SMARTPIPE_CRASH_TOKEN \
  SMARTPIPE_DSN LANXIN_APP_ID LANXIN_APP_SECRET; do
  value="$(get_value "$key")"
  if [[ -z "$value" ]]; then
    echo "FAIL empty setting: $key" >&2
    fail=1
  fi
done
if [[ "$(get_value INTERNAL_TOKEN)" != "$(get_value SMARTPIPE_BOT_INTERNAL_TOKEN)" ]]; then
  echo "FAIL INTERNAL_TOKEN does not match SMARTPIPE_BOT_INTERNAL_TOKEN" >&2
  fail=1
fi
if [[ "$(get_value BRIDGE_TOKEN)" != "$(get_value SMARTPIPE_BRIDGE_TOKEN)" ]]; then
  echo "FAIL BRIDGE_TOKEN does not match SMARTPIPE_BRIDGE_TOKEN" >&2
  fail=1
fi
if [[ "$(get_value CRASH_INGEST_TOKEN)" != "$(get_value SMARTPIPE_CRASH_TOKEN)" ]]; then
  echo "FAIL CRASH_INGEST_TOKEN does not match SMARTPIPE_CRASH_TOKEN" >&2
  fail=1
fi

if [[ "$SECRETS_ONLY" -eq 1 ]]; then
  if [[ "$fail" -ne 0 ]]; then
    echo "ENV VERIFY FAILED" >&2
    exit 1
  fi
  echo "ENV VERIFY OK"
  exit 0
fi

if [[ "$MODE" == "docker" ]]; then
  compose_dir="$INSTALL_DIR/deploy"
  if [[ ! -f "$compose_dir/docker-compose.yml" ]]; then
    echo "FAIL compose file missing" >&2
    exit 1
  fi
  (cd "$compose_dir" && docker compose config -q)
  for service in admin-api bridge patch-verifier bot; do
    cid="$(cd "$compose_dir" && docker compose ps -q "$service")"
    if [[ -z "$cid" ]]; then
      echo "FAIL missing container: $service" >&2
      fail=1
      continue
    fi
    state="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid")"
    if [[ "$state" != "healthy" && "$state" != "running" ]]; then
      echo "FAIL container state: $service=$state" >&2
      fail=1
    else
      echo "CONTAINER $service=$state"
    fi
  done
  for pair in "admin-api:8092" "bridge:8090" "patch-verifier:8095" "bot:8093"; do
    service="${pair%%:*}"
    port="${pair##*:}"
    if ! (cd "$compose_dir" && docker compose exec -T "$service" wget -qO- "http://127.0.0.1:$port/healthz"); then
      echo "FAIL health: $service" >&2
      fail=1
    fi
    printf '\n'
  done
  (cd "$compose_dir" && docker compose ps)
else
  for unit in smartpipe-patch-verifier smartpipe-admin-api smartpipe-bridge smartpipe-bot; do
    if systemctl is-active --quiet "$unit.service"; then
      echo "SERVICE $unit=active"
    else
      echo "FAIL service inactive: $unit" >&2
      fail=1
    fi
  done
  for port in 8092 8090 8095 8097 8093; do
    if ! curl -fsS --max-time 5 "http://127.0.0.1:$port/healthz"; then
      echo "FAIL health on port $port" >&2
      fail=1
    fi
    printf '\n'
  done
fi

if [[ "$fail" -ne 0 ]]; then
  echo "DEPLOY VERIFY FAILED" >&2
  exit 1
fi
echo "DEPLOY VERIFY OK"
