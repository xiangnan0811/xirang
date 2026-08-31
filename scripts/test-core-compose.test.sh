#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SMOKE="$ROOT_DIR/scripts/test-core-compose.sh"
DOCKERFILE="$ROOT_DIR/deploy/allinone/Dockerfile"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

fail() {
  echo "core Compose smoke self-test: $*" >&2
  exit 1
}

if [[ ! -f "$SMOKE" ]]; then
  fail "smoke script not found: $SMOKE"
fi
if [[ ! -f "$DOCKERFILE" ]]; then
  fail "all-in-one Dockerfile not found: $DOCKERFILE"
fi
if ! grep -Fq -- 'ADMIN_INITIAL_PASSWORD=CoreSmokeAdmin1!' "$SMOKE"; then
  fail "smoke fixture is missing the required initial admin password"
fi

runtime_packages=$(
  awk '
    /^FROM nginx:[^[:space:]]+@sha256:/ { in_runtime = 1; next }
    in_runtime && /^RUN apk add --no-cache / { in_packages = 1 }
    in_packages { print }
    in_packages && $0 !~ /\\$/ { exit }
  ' "$DOCKERFILE"
)
if [[ -z "$runtime_packages" ]]; then
  fail "all-in-one runtime package install block not found"
fi
for package in \
  'c-ares=1.34.8-r0' \
  'libcrypto3=3.5.8-r0' \
  'libssl3=3.5.8-r0' \
  'libexpat=2.8.3-r0' \
  'libxml2=2.13.9-r1' \
  'nghttp2-libs=1.69.0-r0'
do
  if ! awk -v expected="$package" '
    {
      for (field = 1; field <= NF; field++) {
        if ($field ~ /^#/) {
          break
        }
        if ($field == expected) {
          found = 1
        }
      }
    }
    END { exit(found ? 0 : 1) }
  ' <<<"$runtime_packages"; then
    fail "all-in-one runtime package is not fixed-version pinned: $package"
  fi
done
if grep -Eq '^[[:space:]]*RUN[[:space:]].*apk[[:space:]]+upgrade|apk[[:space:]]+upgrade' "$DOCKERFILE"; then
  fail "all-in-one Dockerfile uses nondeterministic apk upgrade"
fi

assert_runtime_pin_mutation_rejected() {
  local fixture_name=$1
  local replacement=$2
  local failure_message=$3
  local mutation_root="$TMP_DIR/$fixture_name"
  local mutation_test="$mutation_root/scripts/test-core-compose.test.sh"
  local mutation_log="$TMP_DIR/$fixture_name.log"

  mkdir -p "$mutation_root/scripts" "$mutation_root/deploy/allinone"
  cp "$0" "$mutation_test"
  cp "$SMOKE" "$mutation_root/scripts/test-core-compose.sh"
  cp "$ROOT_DIR/docker-compose.yml" "$mutation_root/docker-compose.yml"
  sed "s|c-ares=1\.34\.8-r0|$replacement|" "$DOCKERFILE" \
    >"$mutation_root/deploy/allinone/Dockerfile"
  if CORE_COMPOSE_RUNTIME_PIN_MUTATION_CHILD=1 bash "$mutation_test" >"$mutation_log" 2>&1; then
    fail "$failure_message"
  fi
}

if [[ "${CORE_COMPOSE_RUNTIME_PIN_MUTATION_CHILD:-0}" != "1" ]]; then
  assert_runtime_pin_mutation_rejected \
    runtime-pin-substring-mutation \
    not-c-ares=1.34.8-r0 \
    "runtime package guard accepted a longer token containing the expected pin"
  assert_runtime_pin_mutation_rejected \
    runtime-pin-comment-mutation \
    '# c-ares=1.34.8-r0' \
    "runtime package guard accepted an expected pin from a comment"
fi

DOCKER_LOG="$TMP_DIR/docker.log"
CURL_LOG="$TMP_DIR/curl.log"
FAKE_DOCKER="$TMP_DIR/docker"
FAKE_CURL="$TMP_DIR/curl"

cat >"$FAKE_DOCKER" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$MOCK_DOCKER_LOG"
case "$*" in
  "info") exit 0 ;;
  "ps -a --filter name=^/xirang$ --format {{.ID}}")
    if [[ "${MOCK_EXISTING_CORE:-0}" == "1" ]]; then
      echo existing-core
    fi
    ;;
  *" up -d --no-build xirang")
    if [[ "${MOCK_UP_FAILURE:-0}" == "1" ]]; then
      exit 1
    fi
    ;;
  *" ps -q xirang") echo core-smoke-container ;;
	  "inspect --format {{.State.Health.Status}} core-smoke-container") echo healthy ;;
	  "logs core-smoke-container")
	    if [[ "${MOCK_PROCESSING_WARNING:-0}" == "1" ]]; then
	      echo 'module=backup_asset_processing stage=startup 备份资产处理运行时不可用'
	    fi
	    ;;
	  *" ps --services --status running") echo xirang ;;
esac
DOCKER

cat >"$FAKE_CURL" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$MOCK_CURL_LOG"
printf '{"status":"ok"}\n'
CURL
chmod +x "$FAKE_DOCKER" "$FAKE_CURL"

run_smoke() {
  MOCK_DOCKER_LOG="$DOCKER_LOG" \
  MOCK_CURL_LOG="$CURL_LOG" \
  CORE_COMPOSE_DOCKER="$FAKE_DOCKER" \
  CORE_COMPOSE_CURL="$FAKE_CURL" \
  CORE_COMPOSE_PROJECT=xirang-core-smoke-selftest \
  CORE_COMPOSE_IMAGE_TAG=ci-core-smoke \
  bash "$SMOKE"
}

run_smoke

if ! grep -Fq -- 'up -d --no-build xirang' "$DOCKER_LOG"; then
  fail "smoke did not start only the core service"
fi
if ! grep -Fq -- 'down --volumes --remove-orphans' "$DOCKER_LOG"; then
  fail "smoke did not perform targeted project cleanup"
fi
if ! grep -Fq -- 'run --rm --network none --user 0:0 --read-only --cap-drop ALL --cap-add DAC_OVERRIDE' "$DOCKER_LOG"; then
  fail "smoke did not clean Core-owned bind data through an isolated helper"
fi
if grep -Eq -- '(^| )(system prune|container prune|rm -f|--profile asset-worker)( |$)' "$DOCKER_LOG"; then
  fail "smoke used broad cleanup or enabled the Worker profile"
fi
if ! grep -Fq -- 'http://127.0.0.1:10761/readyz' "$CURL_LOG"; then
  fail "smoke did not probe the readiness endpoint"
fi
if ! grep -Fq -- 'http://127.0.0.1:10761/healthz' "$CURL_LOG"; then
  fail "smoke did not preserve the external liveness endpoint"
fi
if ! grep -Fq -- 'logs core-smoke-container' "$DOCKER_LOG"; then
  fail "smoke did not inspect Core logs for disabled processing startup noise"
fi

: >"$DOCKER_LOG"
if MOCK_PROCESSING_WARNING=1 run_smoke >"$TMP_DIR/processing-warning.log" 2>&1; then
  fail "smoke accepted disabled processing startup warning noise"
fi

: >"$DOCKER_LOG"
if MOCK_UP_FAILURE=1 run_smoke >"$TMP_DIR/up-failure.log" 2>&1; then
  fail "smoke accepted a failed Compose startup"
fi
if ! grep -Fq -- 'down --volumes --remove-orphans' "$DOCKER_LOG"; then
  fail "smoke did not clean its project after a partial Compose startup"
fi

: >"$DOCKER_LOG"
if MOCK_EXISTING_CORE=1 run_smoke >"$TMP_DIR/existing.log" 2>&1; then
  fail "smoke accepted a pre-existing container named xirang"
fi
if grep -Fq -- 'down --volumes --remove-orphans' "$DOCKER_LOG"; then
  fail "smoke attempted cleanup after detecting a pre-existing core"
fi

if CORE_COMPOSE_PROJECT='*' CORE_COMPOSE_DOCKER="$FAKE_DOCKER" bash "$SMOKE" >"$TMP_DIR/project.log" 2>&1; then
  fail "smoke accepted an unsafe Compose project name"
fi

echo "core Compose smoke self-test: PASS"
