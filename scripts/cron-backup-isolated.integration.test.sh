#!/usr/bin/env bash
# Run the destructive cron round trip only against this invocation's Docker database.
set -Eeuo pipefail
umask 077

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
POSTGRES_IMAGE='postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
WORK=
RUN_TOKEN=
RUN_LABEL=
NETWORK_NAME=
CREATION_ATTEMPTED=0
ACTIVE_PID=
ACTIVE_CREATING=0
LAUNCHING=0
SIGNAL_STATUS=0
SUITE_PASSED=0

fail() {
  echo "isolated cron integration: $*" >&2
  exit 1
}

cleanup_containers() {
  local ids id identity tools_id= postgres_id= remaining status=0
  if ! ids=$(docker ps --all --quiet --no-trunc --filter "label=$RUN_LABEL"); then
    echo "isolated cron integration: cannot list owned containers ($RUN_LABEL)" >&2
    return 1
  fi
  for id in $ids; do
    if ! identity=$(docker inspect --format '{{.Name}} {{index .Config.Labels "xirang.cron-isolation"}}' "$id"); then
      echo "isolated cron integration: cannot identify container $id" >&2
      status=1
      continue
    fi
    case "$identity" in
      "/${NETWORK_NAME}-tools $RUN_TOKEN")
        if [[ -n "$tools_id" ]]; then status=1; else tools_id=$id; fi ;;
      "/${NETWORK_NAME}-postgres $RUN_TOKEN")
        if [[ -n "$postgres_id" ]]; then status=1; else postgres_id=$id; fi ;;
      *)
        echo "isolated cron integration: refusing unexpected labeled container $id ($identity)" >&2
        status=1 ;;
    esac
  done
  for id in "$tools_id" "$postgres_id"; do
    [[ -n "$id" ]] || continue
    if docker rm --force --volumes "$id" >/dev/null; then
      echo "isolated cron integration: removed container $id"
    else
      echo "isolated cron integration: failed to remove container $id" >&2
      status=1
    fi
  done
  if ! remaining=$(docker ps --all --quiet --no-trunc --filter "label=$RUN_LABEL"); then
    echo "isolated cron integration: cannot verify container cleanup ($RUN_LABEL)" >&2
    status=1
  elif [[ -n "$remaining" ]]; then
    echo "isolated cron integration: containers remain for $RUN_LABEL: $remaining" >&2
    status=1
  fi
  return "$status"
}

cleanup_networks() {
  local ids id identity remaining status=0
  if ! ids=$(docker network ls --quiet --no-trunc --filter "label=$RUN_LABEL"); then
    echo "isolated cron integration: cannot list owned networks ($RUN_LABEL)" >&2
    return 1
  fi
  for id in $ids; do
    if ! identity=$(docker network inspect --format '{{.Name}} {{index .Labels "xirang.cron-isolation"}}' "$id"); then
      echo "isolated cron integration: cannot identify network $id" >&2
      status=1
      continue
    fi
    if [[ "$identity" != "$NETWORK_NAME $RUN_TOKEN" ]]; then
      echo "isolated cron integration: refusing unexpected labeled network $id ($identity)" >&2
      status=1
    elif docker network rm "$id" >/dev/null; then
      echo "isolated cron integration: removed network $id"
    else
      echo "isolated cron integration: failed to remove network $id" >&2
      status=1
    fi
  done
  if ! remaining=$(docker network ls --quiet --no-trunc --filter "label=$RUN_LABEL"); then
    echo "isolated cron integration: cannot verify network cleanup ($RUN_LABEL)" >&2
    status=1
  elif [[ -n "$remaining" ]]; then
    echo "isolated cron integration: networks remain for $RUN_LABEL: $remaining" >&2
    status=1
  fi
  return "$status"
}

cleanup() {
  local status=$?
  trap - EXIT
  trap '' INT TERM HUP

  # A create request can have succeeded even if its client was interrupted.
  # Let that request finish before discovering this invocation's labeled objects.
  if [[ -n "$ACTIVE_PID" ]]; then
    if [[ "$ACTIVE_CREATING" -ne 1 ]]; then
      # The Docker attach client forwards catchable signals to the container.
      # Stop only this client first; the owned container is removed below.
      kill -KILL "$ACTIVE_PID" 2>/dev/null || true
    fi
    wait "$ACTIVE_PID" 2>/dev/null || true
    ACTIVE_PID=
  fi
  if [[ "$CREATION_ATTEMPTED" -eq 1 ]]; then
    cleanup_containers || status=1
    cleanup_networks || status=1
  fi
  if [[ -n "$WORK" ]] && ! rm -rf -- "$WORK"; then
    echo "isolated cron integration: failed to remove temporary directory $WORK" >&2
    status=1
  fi
  if [[ "$status" -eq 0 && "$SUITE_PASSED" -eq 1 ]]; then
    echo "isolated cron integration: PASS (owned resources removed)"
  fi
  exit "$status"
}
trap cleanup EXIT
signal_received() {
  SIGNAL_STATUS=$1
  if [[ "$LAUNCHING" -ne 1 ]]; then
    exit "$SIGNAL_STATUS"
  fi
}
trap 'signal_received 130' INT
trap 'signal_received 143' TERM
trap 'signal_received 129' HUP

for required in docker jq od tr mktemp; do
  command -v "$required" >/dev/null 2>&1 || fail "$required is required"
done
docker info >/dev/null 2>&1 || fail "Docker daemon is required"
GO_IMAGE=$(jq -er '
  . as $lock | .bases.go |
  select(.source | test("^golang:1\\.27\\.3-alpine[^@]*@sha256:[0-9a-f]{64}$")) |
  .ref | select(type == "string" and startswith($lock.registry + "@sha256:") and
    test("^[^[:space:]]+@sha256:[0-9a-f]{64}$"))
' "$ROOT_DIR/deploy/build-inputs/lock.json") || fail "retained Go 1.27.3 Alpine base is missing or invalid"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/xirang-cron-isolated.XXXXXXXXXXXX") || fail "cannot create private temporary directory"
RUN_TOKEN=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
[[ "$RUN_TOKEN" =~ ^[0-9a-f]{32}$ ]] || fail "cannot generate a random isolation identity"
RUN_LABEL="xirang.cron-isolation=$RUN_TOKEN"
NETWORK_NAME="xirang-cron-$RUN_TOKEN"
echo "isolated cron integration: owner $RUN_LABEL"
echo "isolated cron integration: postgres image $POSTGRES_IMAGE"
echo "isolated cron integration: retained tools image $GO_IMAGE"

# Pull before creating resources, so cancellation during a pull has nothing to clean up.
ensure_image() {
  local image=$1 result=0
  if docker image inspect "$image" >/dev/null 2>&1; then
    return 0
  fi
  LAUNCHING=1
  docker pull "$image" &
  ACTIVE_PID=$!
  LAUNCHING=0
  [[ "$SIGNAL_STATUS" -eq 0 ]] || exit "$SIGNAL_STATUS"
  wait "$ACTIVE_PID" || result=$?
  ACTIVE_PID=
  [[ "$result" -eq 0 ]] || fail "cannot pull required image $image"
}
ensure_image "$POSTGRES_IMAGE"
ensure_image "$GO_IMAGE"

# Capture create results outside command substitution so a signal can interrupt
# wait; cleanup then waits for the create and recovers its objects by owner label.
create_owned() {
  local output=$1 result=0
  shift
  ACTIVE_CREATING=1
  LAUNCHING=1
  "$@" >"$output" &
  ACTIVE_PID=$!
  LAUNCHING=0
  [[ "$SIGNAL_STATUS" -eq 0 ]] || exit "$SIGNAL_STATUS"
  wait "$ACTIVE_PID" || result=$?
  ACTIVE_PID=
  ACTIVE_CREATING=0
  return "$result"
}

CREATION_ATTEMPTED=1
create_owned "$WORK/network.id" docker network create --label "$RUN_LABEL" "$NETWORK_NAME" || fail "cannot create isolated network"
NETWORK_ID=$(<"$WORK/network.id")
[[ "$NETWORK_ID" =~ ^[0-9a-f]{64}$ ]] || fail "Docker returned an invalid network ID"
echo "isolated cron integration: network $NETWORK_NAME $NETWORK_ID"

create_owned "$WORK/postgres.id" docker create \
  --name "${NETWORK_NAME}-postgres" --label "$RUN_LABEL" \
  --network "$NETWORK_ID" --network-alias postgres \
  --tmpfs /var/lib/postgresql:rw,nosuid,noexec,uid=70,gid=70,mode=0700 \
  --env POSTGRES_DB=xirang_cron_test \
  --env POSTGRES_PASSWORD=FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY \
  "$POSTGRES_IMAGE" || fail "cannot create isolated PostgreSQL container"
POSTGRES_ID=$(<"$WORK/postgres.id")
[[ "$POSTGRES_ID" =~ ^[0-9a-f]{64}$ ]] || fail "Docker returned an invalid PostgreSQL container ID"
POSTGRES_IMAGE_ID=$(docker inspect --format '{{.Image}}' "$POSTGRES_ID") || fail "cannot inspect PostgreSQL image identity"
[[ "$POSTGRES_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "Docker returned an invalid PostgreSQL image identity"
echo "isolated cron integration: postgres container $POSTGRES_ID image $POSTGRES_IMAGE_ID"
docker start "$POSTGRES_ID" >/dev/null || fail "cannot start isolated PostgreSQL container"

ready=0
for ((attempt=1; attempt<=60; attempt++)); do
  if docker exec "$POSTGRES_ID" pg_isready -h 127.0.0.1 -U postgres -d xirang_cron_test >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" -ne 1 ]]; then
  docker logs "$POSTGRES_ID" >&2 || true
  fail "isolated PostgreSQL did not become ready"
fi

TOOLS_COMMAND=$(cat <<'TOOL_SCRIPT'
set -euo pipefail
umask 077
export CGO_ENABLED=1 GOTOOLCHAIN=local
export TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@postgres:5432/xirang_cron_test?sslmode=disable'

[[ "$(go env GOVERSION)" == go1.27.3 && "$(go env CGO_ENABLED)" == 1 ]] || {
  echo 'isolated cron integration: unexpected Go or CGO version' >&2
  exit 1
}
go version
sqlite3 --version
for tool in psql pg_dump pg_restore; do
  version=$("$tool" --version)
  echo "$version"
  case "$version" in
    "$tool (PostgreSQL) 18"|"$tool (PostgreSQL) 18."*) ;;
    *) echo "isolated cron integration: $tool is not PostgreSQL 18" >&2; exit 1 ;;
  esac
done
server_version=$(psql -X --set ON_ERROR_STOP=on --tuples-only --no-align \
  --dbname "$TEST_POSTGRES_DSN" --command 'SHOW server_version_num')
[[ "$server_version" =~ ^18[0-9]{4}$ ]] || {
  echo 'isolated cron integration: server is not PostgreSQL 18' >&2
  exit 1
}
echo "PostgreSQL server_version_num=$server_version"

# Keep the checkout read-only even when it has no .tmp directory.  The suite's
# root and all of its generated artifacts live in the private executable tmpfs.
ln -s /source/backend /work/backend
ln -s /source/scripts /work/scripts
mkdir -p /work/.tmp/agent
exec bash /work/scripts/cron-backup.integration.test.sh
TOOL_SCRIPT
)

create_owned "$WORK/tools.id" docker create \
  --name "${NETWORK_NAME}-tools" --label "$RUN_LABEL" \
  --network "$NETWORK_ID" \
  --mount "type=bind,src=$ROOT_DIR,dst=/source,readonly" \
  --tmpfs /work:rw,nosuid,nodev,exec,mode=0700 \
  --entrypoint /bin/sh "$GO_IMAGE" -ec \
  'apk add --no-cache bash postgresql18-client sqlite coreutils findutils build-base; exec bash -euo pipefail -c "$1"' \
  sh "$TOOLS_COMMAND" || fail "cannot create disposable tools container"
TOOLS_ID=$(<"$WORK/tools.id")
[[ "$TOOLS_ID" =~ ^[0-9a-f]{64}$ ]] || fail "Docker returned an invalid tools container ID"
TOOLS_IMAGE_ID=$(docker inspect --format '{{.Image}}' "$TOOLS_ID") || fail "cannot inspect tools image identity"
[[ "$TOOLS_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "Docker returned an invalid tools image identity"
echo "isolated cron integration: tools container $TOOLS_ID image $TOOLS_IMAGE_ID"

LAUNCHING=1
docker start --attach "$TOOLS_ID" &
ACTIVE_PID=$!
LAUNCHING=0
[[ "$SIGNAL_STATUS" -eq 0 ]] || exit "$SIGNAL_STATUS"
result=0
wait "$ACTIVE_PID" || result=$?
ACTIVE_PID=
[[ "$result" -eq 0 ]] || fail "cron integration suite or tools setup failed (exit $result)"
SUITE_PASSED=1
