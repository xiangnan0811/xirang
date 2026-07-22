#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
COMPOSE_FILE=${COMPOSE_FILE_PATH:-"$ROOT_DIR/docker-compose.yml"}
DOCKER=${CORE_COMPOSE_DOCKER:-docker}
CURL=${CORE_COMPOSE_CURL:-curl}
PROJECT=${CORE_COMPOSE_PROJECT:-xirang-core-smoke}
IMAGE_TAG=${CORE_COMPOSE_IMAGE_TAG:-}
TMP_DIR=$(mktemp -d)
STARTED=0

fail() {
  echo "core-only Compose smoke: $*" >&2
  exit 1
}

cleanup() {
  if [[ "$STARTED" == "1" ]]; then
    IMAGE_TAG="$IMAGE_TAG" "$DOCKER" compose -p "$PROJECT" -f "$TMP_DIR/docker-compose.yml" \
      down --volumes --remove-orphans >/dev/null 2>&1 || true
    "$DOCKER" run --rm \
      --network none \
      --user 0:0 \
      --read-only \
      --cap-drop ALL \
      --cap-add DAC_OVERRIDE \
      --security-opt no-new-privileges=true \
      --entrypoint /bin/sh \
      -v "$TMP_DIR:/smoke" \
      "linnea7171/xirang:$IMAGE_TAG" -eu -c 'rm -rf /smoke/* /smoke/.[!.]* /smoke/..?*' \
      >/dev/null 2>&1 || true
  fi
  rm -rf -- "$TMP_DIR"
}
trap cleanup EXIT

if [[ ! "$PROJECT" =~ ^[a-z0-9][a-z0-9_-]{0,62}$ ]]; then
  fail "unsafe project name"
fi
if [[ ! "$IMAGE_TAG" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
  fail "CORE_COMPOSE_IMAGE_TAG must name an already built local image"
fi
if [[ ! -f "$COMPOSE_FILE" ]]; then
  fail "Compose file not found"
fi
if ! "$DOCKER" info >/dev/null 2>&1; then
  fail "Docker daemon is required"
fi
if [[ -n "$("$DOCKER" ps -a --filter 'name=^/xirang$' --format '{{.ID}}')" ]]; then
  fail "a pre-existing container named xirang is present"
fi

cp "$COMPOSE_FILE" "$TMP_DIR/docker-compose.yml"
cat >"$TMP_DIR/.env" <<'ENV'
APP_ENV=production
JWT_SECRET=core-smoke-jwt-secret-at-least-32-bytes
DATA_ENCRYPTION_KEY=core-smoke-encryption-key
METRICS_TOKEN=core-smoke-metrics-token
ADMIN_INITIAL_PASSWORD=CoreSmokeAdmin1!
BACKUP_ASSETS_ENABLED=false
BACKUP_ASSETS_WORKER_LOCAL_ENABLED=false
BACKUP_ASSETS_WORKER_UPDATER_ENABLED=false
ENV
mkdir -p "$TMP_DIR/data" "$TMP_DIR/backups" "$TMP_DIR/logs"

STARTED=1
IMAGE_TAG="$IMAGE_TAG" "$DOCKER" compose -p "$PROJECT" -f "$TMP_DIR/docker-compose.yml" \
  up -d --no-build xirang

container_id=$(IMAGE_TAG="$IMAGE_TAG" "$DOCKER" compose -p "$PROJECT" -f "$TMP_DIR/docker-compose.yml" ps -q xirang)
if [[ -z "$container_id" ]]; then
  fail "core container was not created"
fi

healthy=0
for _ in $(seq 1 60); do
  status=$("$DOCKER" inspect --format '{{.State.Health.Status}}' "$container_id" 2>/dev/null || true)
  if [[ "$status" == "healthy" ]]; then
    healthy=1
    break
  fi
  if [[ "$status" == "unhealthy" ]]; then
    break
  fi
  sleep 1
done
if [[ "$healthy" != "1" ]]; then
  fail "core healthcheck did not become healthy"
fi

running=$(IMAGE_TAG="$IMAGE_TAG" "$DOCKER" compose -p "$PROJECT" -f "$TMP_DIR/docker-compose.yml" \
  ps --services --status running)
if [[ "$running" != "xirang" ]]; then
  fail "core-only smoke started optional services"
fi
core_logs=$("$DOCKER" logs "$container_id" 2>&1)
if grep -Eq 'module=backup_asset_processing.*stage=startup|备份资产处理运行时不可用' <<<"$core_logs"; then
  fail "disabled processing emitted startup failure noise"
fi
"$CURL" -fsS --max-time 5 http://127.0.0.1:10761/healthz >/dev/null

echo "core-only Compose smoke: PASS"
