#!/usr/bin/env bash
# Deploy / refresh EchoEar Cloud on this host.
# Invoked by GitHub Actions through a dedicated, command-restricted SSH key.
set -euo pipefail

STACK_DIR="${ECHOEAR_STACK_DIR:-/opt/stack/echoear_cloud}"
COMPOSE_FILE="$STACK_DIR/docker-compose.yml"
ENV_FILE="$STACK_DIR/.env"
DEFAULT_IMAGE="ghcr.io/18345174/echoear_cloud:latest"
log() { printf '[echoear-deploy] %s\n' "$*"; }

if [[ ! -d "$STACK_DIR" ]]; then
  log "ERROR: stack dir missing: $STACK_DIR"
  exit 1
fi
if [[ ! -f "$COMPOSE_FILE" ]]; then
  log "ERROR: compose file missing: $COMPOSE_FILE"
  exit 1
fi
if [[ ! -f "$ENV_FILE" ]]; then
  log "ERROR: env file missing: $ENV_FILE"
  exit 1
fi

cd "$STACK_DIR"

env_file_value() {
  grep -E "^$1=" "$ENV_FILE" | tail -n1 | cut -d= -f2- || true
}

DEPLOY_REF="${ECHOEAR_DEPLOY_REF:-$(env_file_value ECHOEAR_DEPLOY_REF)}"
DEPLOY_REF="${DEPLOY_REF:-main}"
DEPLOY_REPOSITORY="${ECHOEAR_DEPLOY_REPOSITORY:-$(env_file_value ECHOEAR_DEPLOY_REPOSITORY)}"
DEPLOY_REPOSITORY="${DEPLOY_REPOSITORY:-18345174/echoear_cloud}"
RAW_BASE_URL="https://raw.githubusercontent.com/$DEPLOY_REPOSITORY/$DEPLOY_REF/deploy/oci"

download_deploy_file() {
  local name="$1"
  local destination="$STACK_DIR/$name"
  local temporary="$destination.next"

  log "syncing $name from $DEPLOY_REPOSITORY@$DEPLOY_REF"
  curl --fail --silent --show-error --location \
    --connect-timeout 10 --max-time 30 \
    "$RAW_BASE_URL/$name" --output "$temporary"
  if [[ ! -s "$temporary" ]]; then
    log "ERROR: downloaded deployment file is empty: $name"
    rm -f "$temporary"
    exit 1
  fi
  if [[ "$name" == *.sh ]]; then
    bash -n "$temporary"
    chmod 0755 "$temporary"
  fi
  if [[ "$name" == "docker-compose.yml" ]]; then
    docker compose --env-file "$ENV_FILE" -f "$temporary" --profile relay config --quiet
  fi
}

# The force-command SSH key invokes the already-installed script. Once this
# version has been bootstrapped, every subsequent deploy refreshes the
# versioned Compose manifest and the script itself before pulling images.
sync_deploy_files="${ECHOEAR_SYNC_DEPLOY_FILES:-$(env_file_value ECHOEAR_SYNC_DEPLOY_FILES)}"
sync_deploy_files="$(printf '%s' "${sync_deploy_files:-true}" | tr '[:upper:]' '[:lower:]')"
if [[ "$sync_deploy_files" == "true" && "${ECHOEAR_DEPLOY_SYNCED:-}" != "1" ]]; then
  download_deploy_file docker-compose.yml
  download_deploy_file deploy.sh
  download_deploy_file validate-relay.sh
  mv "$STACK_DIR/docker-compose.yml.next" "$STACK_DIR/docker-compose.yml"
  mv "$STACK_DIR/deploy.sh.next" "$STACK_DIR/deploy.sh"
  mv "$STACK_DIR/validate-relay.sh.next" "$STACK_DIR/validate-relay.sh"
  export ECHOEAR_DEPLOY_SYNCED=1
  exec "$STACK_DIR/deploy.sh" "$@"
fi

# Resolve image:
# 1) explicit CLI arg when run interactively: ./deploy.sh ghcr.io/...:tag
# 2) ECHOEAR_IMAGE already in environment
# 3) ECHOEAR_IMAGE from .env
# 4) default latest
IMAGE="${1:-}"
if [[ -z "$IMAGE" && -n "${ECHOEAR_IMAGE:-}" ]]; then
  IMAGE="$ECHOEAR_IMAGE"
fi
if [[ -z "$IMAGE" ]]; then
  IMAGE="$(grep -E '^ECHOEAR_IMAGE=' "$ENV_FILE" | tail -n1 | cut -d= -f2- || true)"
fi
if [[ -z "$IMAGE" ]]; then
  IMAGE="$DEFAULT_IMAGE"
fi

# Never treat unrestricted remote shell text as an image ref.
# Force-command SSH keys always run this script with no safe args.
export ECHOEAR_IMAGE="$IMAGE"

relay_enabled="$(env_file_value ECHOEAR_RELAY_ENABLED)"
relay_enabled="$(printf '%s' "$relay_enabled" | tr '[:upper:]' '[:lower:]')"
COMPOSE_PROFILES=""
if [[ "$relay_enabled" == "true" || "$relay_enabled" == "1" ]]; then
  COMPOSE_PROFILES="relay"
  for name in ECHOEAR_RELAY_DOMAIN ECHOEAR_RELAY_PUBLIC_IP ECHOEAR_RELAY_AUTH_SECRET; do
    value="$(env_file_value "$name")"
    if [[ -z "$value" ]]; then
      log "ERROR: $name is required when ECHOEAR_RELAY_ENABLED=true"
      exit 1
    fi
  done
fi
export COMPOSE_PROFILES

log "stack=$STACK_DIR"
log "image=$ECHOEAR_IMAGE"
log "deploy_ref=$DEPLOY_REF relay_enabled=${relay_enabled:-false}"
log "host=$(hostname) arch=$(uname -m)"

if [[ "$COMPOSE_PROFILES" != "relay" ]]; then
  log "ensuring optional relay service is stopped"
  docker compose --env-file "$ENV_FILE" --profile relay stop relay || true
  docker compose --env-file "$ENV_FILE" --profile relay rm --force relay || true
fi

log "pulling images..."
docker compose --env-file "$ENV_FILE" pull

log "recreating services..."
docker compose --env-file "$ENV_FILE" up -d --remove-orphans

log "waiting for api health..."
ready=0
for i in $(seq 1 36); do
  if curl -fsS "http://127.0.0.1:18080/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 5
done

log "compose status:"
docker compose --env-file "$ENV_FILE" ps

if [[ "$ready" -ne 1 ]]; then
  log "ERROR: api health check failed"
  docker compose --env-file "$ENV_FILE" logs --tail=80 api || true
  exit 1
fi

log "health:"
curl -fsS "http://127.0.0.1:18080/healthz" || true
echo

if [[ "$COMPOSE_PROFILES" == "relay" ]]; then
  relay_domain="$(env_file_value ECHOEAR_RELAY_DOMAIN)"
  log "relay registration endpoint: https://$relay_domain/issue"
  for i in $(seq 1 24); do
    relay_status="$(curl --silent --output /dev/null --write-out '%{http_code}' \
      --connect-timeout 5 --max-time 10 "https://$relay_domain/issue" || true)"
    if [[ "$relay_status" == "405" ]]; then
      log "relay health ok (HTTP $relay_status)"
      break
    fi
    if [[ "$i" == "24" ]]; then
      log "ERROR: relay health check failed (last HTTP ${relay_status:-000})"
      docker compose --env-file "$ENV_FILE" logs --tail=100 relay || true
      exit 1
    fi
    sleep 5
  done
fi

log "deploy ok"
