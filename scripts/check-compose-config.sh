#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
COMPOSE_FILE=${COMPOSE_FILE_PATH:-"$ROOT_DIR/docker-compose.yml"}
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

fail() {
  echo "Compose contract check: $*" >&2
  exit 1
}

if [[ ! -f "$COMPOSE_FILE" ]]; then
  fail "Compose file not found: $COMPOSE_FILE"
fi
if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  fail "Docker Compose is required"
fi
if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required"
fi

cp "$COMPOSE_FILE" "$TMP_DIR/docker-compose.yml"
cat >"$TMP_DIR/.env" <<'ENV'
APP_ENV=production
JWT_SECRET=compose-check-jwt-secret-at-least-32-bytes
DATA_ENCRYPTION_KEY=compose-check-encryption-key
METRICS_TOKEN=compose-check-metrics-token
BACKUP_ASSETS_ENABLED=false
BACKUP_ASSETS_WORKER_LOCAL_ENABLED=false
BACKUP_ASSETS_WORKER_UPDATER_ENABLED=false
ENV
mkdir -p "$TMP_DIR/asset-worker-inbox"
printf '%s\n' '{"schema_version":1,"keys":[]}' >"$TMP_DIR/asset-worker-updater-trust.json"
chmod 0555 "$TMP_DIR/asset-worker-inbox"
chmod 0440 "$TMP_DIR/asset-worker-updater-trust.json"

if ! (
  cd "$TMP_DIR"
  IMAGE_TAG=compose-check docker compose -f docker-compose.yml config --format json >core.json
  IMAGE_TAG=compose-check docker compose --profile asset-worker -f docker-compose.yml config --format json >profile.json
); then
  fail "Docker Compose could not render core-only and Worker-profile configurations"
fi

if ! grep -Fqx -- '    image: linnea7171/xirang:${IMAGE_TAG:-latest}' "$COMPOSE_FILE"; then
  fail "the official core image selector changed"
fi

jq -e '
  .services.xirang.image == "linnea7171/xirang:compose-check" and
  (.services.xirang.ports | length) == 1 and
  .services.xirang.ports[0].target == 10761 and
  .services.xirang.ports[0].published == "10761" and
  .services.xirang.healthcheck.test == ["CMD", "curl", "-fsS", "http://127.0.0.1:10761/healthz"] and
  .services.xirang.cap_drop == ["FSETID"] and
  .services.xirang.cap_add == null and
  .services.xirang.pid == null and
  ([.services.xirang.volumes[] | select(.source == "asset-worker-derived-store" and .target == "/var/lib/xirang-asset-runtime/derived" and (.read_only // false) == false)] | length) == 1 and
  .volumes["asset-worker-derived-store"] != null and
  .services.xirang.depends_on["asset-worker-init"].required == false and
  .services["asset-worker"] == null and
  .services["asset-worker-updater"] == null
' "$TMP_DIR/core.json" >/dev/null || fail "core-only Compose contract changed"

jq -e '
  .services["asset-worker"].profiles == ["asset-worker"] and
  .services["asset-worker"].user == "10000:10000" and
  .services["asset-worker"].group_add == null and
  .services["asset-worker"].network_mode == "none" and
  .services["asset-worker"].pid == null and
  .services["asset-worker"].read_only == true and
  .services["asset-worker"].cap_drop == ["ALL"] and
  .services["asset-worker"].cap_add == null and
  (.services["asset-worker"].security_opt | index("no-new-privileges:true")) != null and
	  (.services["asset-worker"].security_opt | map(startswith("seccomp=")) | any) and
	  .services["asset-worker"].pids_limit == 256 and
	  .services["asset-worker"].mem_limit == "1073741824" and
	  .services["asset-worker"].memswap_limit == "1073741824" and
	  (.services["asset-worker"].ports == null) and
  ([.services["asset-worker"].volumes[] | select(.target == "/var/lib/xirang/asset-worker-bundles" and .read_only == true)] | length) == 1 and
  ([.services["asset-worker"].volumes[] | select(.source == "asset-worker-worker-runtime" and .target == "/run/xirang/worker" and .read_only == true)] | length) == 1 and
  ([.services["asset-worker"].volumes[] | select(.target == "/run/xirang")] | length) == 0 and
  ([.services["asset-worker"].volumes[] | select(.source == "asset-worker-derived-store" or .target == "/var/lib/xirang-asset-runtime/derived")] | length) == 0 and
  (.services["asset-worker"].tmpfs | sort) == ([
    "/run/xirang/asset-jobs:rw,noexec,nosuid,nodev,size=268435456,nr_inodes=32768,mode=0700,uid=10000,gid=10000",
    "/tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000"
  ] | sort)
' "$TMP_DIR/profile.json" >/dev/null || fail "parser Worker isolation contract changed"

jq -e '
  .services["asset-worker-updater"].profiles == ["asset-worker"] and
  .services["asset-worker-updater"].user == "10002:10002" and
  .services["asset-worker-updater"].network_mode == "none" and
  .services["asset-worker-updater"].pid == null and
  .services["asset-worker-updater"].read_only == true and
  .services["asset-worker-updater"].cap_drop == ["ALL"] and
  .services["asset-worker-updater"].cap_add == null and
  (.services["asset-worker-updater"].security_opt | index("no-new-privileges:true")) != null and
	  (.services["asset-worker-updater"].security_opt | map(startswith("seccomp=")) | any) and
	  .services["asset-worker-updater"].pids_limit == 64 and
	  .services["asset-worker-updater"].mem_limit == "268435456" and
	  .services["asset-worker-updater"].memswap_limit == "268435456" and
	  (.services["asset-worker-updater"].ports == null) and
  ([.services["asset-worker-updater"].volumes[] | select(.target == "/var/lib/xirang/asset-worker-bundles" and (.read_only // false) == false)] | length) == 1 and
  ([.services["asset-worker-updater"].volumes[] | select(.target == "/var/lib/xirang/asset-worker-inbox" and .read_only == true)] | length) == 1 and
  ([.services["asset-worker-updater"].volumes[] | select(.source == "asset-worker-updater-runtime" and .target == "/run/xirang" and .read_only == true)] | length) == 1 and
  ([.services["asset-worker-updater"].volumes[] | select(.target == "/run/xirang/worker")] | length) == 0 and
  ([.services["asset-worker-updater"].volumes[] | select(.source == "asset-worker-derived-store" or .target == "/var/lib/xirang-asset-runtime/derived")] | length) == 0 and
  (.services["asset-worker-updater"].secrets | length) == 1
' "$TMP_DIR/profile.json" >/dev/null || fail "Updater identity or mount contract changed"

jq -e '
  .services["asset-worker-init"].profiles == ["asset-worker"] and
  .services["asset-worker-init"].user == "0:0" and
  .services["asset-worker-init"].network_mode == "none" and
  .services["asset-worker-init"].pid == null and
  .services["asset-worker-init"].read_only == true and
  .services["asset-worker-init"].cap_drop == ["ALL"] and
  (.services["asset-worker-init"].cap_add | sort) == ["CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"] and
  (.services["asset-worker-init"].security_opt | index("no-new-privileges:true")) != null and
  (.services["asset-worker-init"].security_opt | map(startswith("seccomp=")) | any) and
  ([.services["asset-worker-init"].volumes[] | select(.source == "asset-worker-updater-runtime" and .target == "/run/xirang" and (.read_only // false) == false)] | length) == 1 and
  ([.services["asset-worker-init"].volumes[] | select(.source == "asset-worker-worker-runtime" and .target == "/run/xirang/worker" and (.read_only // false) == false)] | length) == 1 and
  ([.services["asset-worker-init"].volumes[] | select(.source == "asset-worker-derived-store" and .target == "/var/lib/xirang-asset-runtime/derived" and (.read_only // false) == false)] | length) == 1 and
  ([.services.xirang.volumes[] | select(.source == "asset-worker-updater-runtime" and .target == "/run/xirang")] | length) == 1 and
  ([.services.xirang.volumes[] | select(.source == "asset-worker-worker-runtime" and .target == "/run/xirang/worker")] | length) == 1 and
  ([.services.xirang.volumes[] | select(.source == "asset-worker-derived-store" and .target == "/var/lib/xirang-asset-runtime/derived")] | length) == 1 and
  .services["asset-worker-init"].restart == "no"
' "$TMP_DIR/profile.json" >/dev/null || fail "profile volume initializer contract changed"

echo "Compose contract check: PASS"
