#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CHECKER="$ROOT_DIR/scripts/check-compose-config.sh"
BASE_COMPOSE="$ROOT_DIR/docker-compose.yml"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

fail() {
  echo "compose checker self-test: $*" >&2
  exit 1
}

if [[ ! -f "$CHECKER" ]]; then
  fail "checker not found: $CHECKER"
fi

run_checker() {
  local compose_file=$1
  COMPOSE_FILE_PATH="$compose_file" bash "$CHECKER"
}

expect_mutation_failure() {
  local name=$1
  local expression=$2
  local candidate="$TMP_DIR/$name.yml"
  local output="$TMP_DIR/$name.log"

  sed "$expression" "$BASE_COMPOSE" >"$candidate"
  if cmp -s "$BASE_COMPOSE" "$candidate"; then
    fail "mutation did not change fixture: $name"
  fi
  if run_checker "$candidate" >"$output" 2>&1; then
    fail "checker accepted unsafe mutation: $name"
  fi
}

run_checker "$BASE_COMPOSE"

expect_mutation_failure core-image-selector 's#linnea7171/xirang:${IMAGE_TAG:-latest}#example.invalid/xirang:latest#'
expect_mutation_failure core-port 's/"10761:10761"/"10762:10761"/'
expect_mutation_failure required-profile-dependency 's/required: false/required: true/'
expect_mutation_failure worker-profile '0,/      - asset-worker/s//      - default/'
expect_mutation_failure worker-uid '0,/user: "10000:10000"/s//user: "10001:10000"/'
expect_mutation_failure worker-network '0,/network_mode: none/s//network_mode: bridge/'
expect_mutation_failure worker-writable-root '0,/read_only: true/s//read_only: false/'
expect_mutation_failure worker-capabilities '0,/      - ALL/s//      - NET_RAW/'
expect_mutation_failure worker-no-new-privileges '0,/no-new-privileges:true/s//no-new-privileges:false/'
expect_mutation_failure worker-seccomp '0,/seccomp=deploy\/worker\/seccomp.json/d'
expect_mutation_failure worker-bundle-writable '/target: \/var\/lib\/xirang\/asset-worker-bundles/{n;s/read_only: true/read_only: false/;}'
expect_mutation_failure updater-identity '0,/user: "10002:10002"/s//user: "10000:10000"/'
expect_mutation_failure worker-public-port '/container_name: xirang-asset-worker/a\    ports: ["9443:9443"]'

echo "compose checker self-test: PASS"
