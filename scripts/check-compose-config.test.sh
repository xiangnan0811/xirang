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

service_block() {
  local service=$1
  awk -v header="  $service:" '
    $0 == header { inside = 1 }
    inside && /^  [A-Za-z0-9_-]+:/ && $0 != header { exit }
    inside { print }
  ' "$BASE_COMPOSE"
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

worker_block=$(service_block asset-worker)
updater_block=$(service_block asset-worker-updater)
core_block=$(service_block xirang)
init_block=$(service_block asset-worker-init)
if grep -Eq -- '^[[:space:]]+target: /run/xirang$' <<<"$worker_block"; then
  fail "parser Worker still mounts the updater runtime root"
fi
if grep -Fq -- 'group_add:' <<<"$worker_block"; then
  fail "parser Worker still joins the updater socket group"
fi
if grep -Fq -- '    pid:' <<<"$worker_block$updater_block$core_block$init_block"; then
  fail "profile services must keep independent PID namespaces"
fi
grep -Fq -- 'source: asset-worker-worker-runtime' <<<"$worker_block" ||
  fail "parser Worker socket volume is not isolated"
grep -Fq -- 'source: asset-worker-updater-runtime' <<<"$updater_block" ||
  fail "updater socket volume is not isolated"
grep -Fq -- 'source: asset-worker-derived-store' <<<"$core_block" ||
  fail "Core does not mount the dedicated Derived Store"
grep -Fq -- 'source: asset-worker-derived-store' <<<"$init_block" ||
  fail "volume initializer does not mount the dedicated Derived Store"
if grep -Fq -- 'source: asset-worker-derived-store' <<<"$worker_block$updater_block"; then
  fail "parser or updater mounts the Core-only Derived Store"
fi

run_checker "$BASE_COMPOSE"

expect_mutation_failure core-image-selector 's#linnea7171/xirang:${IMAGE_TAG:-latest}#example.invalid/xirang:latest#'
expect_mutation_failure core-port 's/"10761:10761"/"10762:10761"/'
expect_mutation_failure core-fsetid-drop '/^  xirang:$/,/^  asset-worker-init:$/ {/      - FSETID/d;}'
expect_mutation_failure core-derived-source '0,/source: asset-worker-derived-store/s//source: asset-worker-bundles/'
expect_mutation_failure core-derived-forbidden-root '0,/target: \/var\/lib\/xirang-asset-runtime\/derived/s//target: \/data\/asset-worker-derived/'
expect_mutation_failure missing-derived-volume '/^  asset-worker-derived-store:$/d'
expect_mutation_failure required-profile-dependency 's/required: false/required: true/'
expect_mutation_failure worker-profile '0,/      - asset-worker/s//      - default/'
expect_mutation_failure worker-uid '0,/user: "10000:10000"/s//user: "10001:10000"/'
expect_mutation_failure worker-network '0,/network_mode: none/s//network_mode: bridge/'
expect_mutation_failure worker-writable-root '0,/read_only: true/s//read_only: false/'
expect_mutation_failure worker-capabilities '0,/      - ALL/s//      - NET_RAW/'
expect_mutation_failure worker-no-new-privileges '0,/no-new-privileges:true/s//no-new-privileges:false/'
expect_mutation_failure worker-seccomp '0,/seccomp=deploy\/worker\/seccomp.json/d'
expect_mutation_failure worker-swap-limit '0,/memswap_limit: 1g/s//memswap_limit: 2g/'
expect_mutation_failure worker-office-tmp-missing '/^  asset-worker:$/,/^  asset-worker-updater:$/ {/      - \/tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000/d;}'
expect_mutation_failure worker-office-tmp-read-only '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#- /tmp:rw,#- /tmp:ro,#;}'
expect_mutation_failure worker-office-tmp-noexec '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#- /tmp:rw,noexec,#- /tmp:rw,#;}'
expect_mutation_failure worker-office-tmp-nosuid '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#noexec,nosuid,nodev#noexec,nodev#;}'
expect_mutation_failure worker-office-tmp-nodev '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#nosuid,nodev,size#nosuid,size#;}'
expect_mutation_failure worker-office-tmp-mode '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#mode=0700,uid=10000#mode=0750,uid=10000#;}'
expect_mutation_failure worker-office-tmp-uid '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#uid=10000,gid=10000#uid=10001,gid=10000#;}'
expect_mutation_failure worker-office-tmp-gid '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#uid=10000,gid=10000#uid=10000,gid=10001#;}'
expect_mutation_failure worker-office-tmp-target '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s#- /tmp:rw,#- /var/tmp:rw,#;}'
expect_mutation_failure init-fsetid '/      - FSETID/d'
expect_mutation_failure worker-bundle-writable '/target: \/var\/lib\/xirang\/asset-worker-bundles/{n;s/read_only: true/read_only: false/;}'
expect_mutation_failure worker-updater-runtime-mount '0,/source: asset-worker-worker-runtime/s//source: asset-worker-updater-runtime/'
expect_mutation_failure worker-derived-store-mount '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s/source: asset-worker-bundles/source: asset-worker-derived-store/;}'
expect_mutation_failure worker-updater-group '/user: "10000:10000"/a\    group_add: ["10002"]'
expect_mutation_failure worker-fsetid '/container_name: xirang-asset-worker$/a\    cap_add: ["FSETID"]'
expect_mutation_failure updater-identity '0,/user: "10002:10002"/s//user: "10000:10000"/'
expect_mutation_failure updater-core-pid '/^  asset-worker-updater:$/,/^volumes:$/ {s/    network_mode: none/    network_mode: none\n    pid: "service:xirang"/;}'
expect_mutation_failure updater-host-pid '/^  asset-worker-updater:$/,/^volumes:$/ {s/    network_mode: none/    network_mode: none\n    pid: host/;}'
expect_mutation_failure worker-core-pid '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s/    network_mode: none/    network_mode: none\n    pid: "service:xirang"/;}'
expect_mutation_failure worker-host-pid '/^  asset-worker:$/,/^  asset-worker-updater:$/ {s/    network_mode: none/    network_mode: none\n    pid: host/;}'
expect_mutation_failure updater-no-new-privileges '/^  asset-worker-updater:$/,/^volumes:$/ {s/no-new-privileges:true/no-new-privileges:false/;}'
expect_mutation_failure updater-seccomp '/^  asset-worker-updater:$/,/^volumes:$/ {/seccomp=deploy\/worker\/seccomp.json/d;}'
expect_mutation_failure updater-swap-limit '/^  asset-worker-updater:$/,/^volumes:$/ {s/memswap_limit: 256m/memswap_limit: 512m/;}'
expect_mutation_failure updater-derived-store-mount '/^  asset-worker-updater:$/,/^volumes:$/ {s/source: asset-worker-bundles/source: asset-worker-derived-store/;}'
expect_mutation_failure updater-fsetid '/container_name: xirang-asset-worker-updater$/a\    cap_add: ["FSETID"]'
expect_mutation_failure init-derived-source '/^  asset-worker-init:$/,/^  asset-worker:$/ {s/source: asset-worker-derived-store/source: asset-worker-bundles/;}'
expect_mutation_failure worker-public-port '/container_name: xirang-asset-worker/a\    ports: ["9443:9443"]'

echo "compose checker self-test: PASS"
