#!/usr/bin/env bash
# Deploy / refresh EchoEar Cloud on this host.
# Invoked by GitHub Actions through a dedicated, command-restricted SSH key.
set -euo pipefail

STACK_DIR="/opt/stack/echoear_cloud"
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

log "stack=$STACK_DIR"
log "image=$ECHOEAR_IMAGE"
log "host=$(hostname) arch=$(uname -m)"

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
log "deploy ok"
