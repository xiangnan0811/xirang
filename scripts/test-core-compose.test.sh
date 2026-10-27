#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SMOKE="$ROOT_DIR/scripts/test-core-compose.sh"
DOCKERFILE="$ROOT_DIR/deploy/allinone/Dockerfile"
BUILD_INPUTS_LOCK="$ROOT_DIR/deploy/build-inputs/lock.json"
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

[[ -f "$BUILD_INPUTS_LOCK" ]] || fail "preserved build input lock not found"
command -v jq >/dev/null 2>&1 || fail "jq is required"
locked_ref() {
  jq -er --arg kind "$1" --arg name "$2" '
    .registry as $registry | .[$kind][$name].ref |
    select(type == "string" and startswith($registry + "@sha256:") and
      test("^[^[:space:]]+@sha256:[0-9a-f]{64}$"))
  ' "$BUILD_INPUTS_LOCK" || fail "invalid retained input reference: $1/$2"
}

grep -Fqx -- "FROM $(locked_ref bases nginx)" "$DOCKERFILE" || fail "Core runtime base differs from the retained lock"

check_offline_apk_profile() {
  local role=$1
  local expected_packages
  local actual_packages
  grep -Fqx -- "FROM $(locked_ref profiles "$role") AS $role-inputs" "$DOCKERFILE" ||
    fail "Core APK archive differs from the retained lock: $role"
  expected_packages=$(jq -er --arg role "$role" '
    .profiles[$role].roots |
    select(type == "array" and length > 0 and
      all(.[]; type == "string" and test("^[A-Za-z0-9][A-Za-z0-9+_.-]*=[^[:space:]=]+$"))) |
    .[]
  ' "$BUILD_INPUTS_LOCK") || fail "Core exact package roots are invalid: $role"
  actual_packages=$(awk -v role="$role" '
    $0 == "RUN --network=none --mount=from=" role "-inputs,source=/inputs,target=/build-inputs \\" {
      blocks++
      state = 1
      next
    }
    state == 1 {
      if ($0 != "    sh /build-inputs/install.sh " role " \"$TARGETARCH\" -- \\") invalid = 1
      state = 2
      next
    }
    state == 2 {
      line = $0
      sub(/[[:space:]]+\\$/, "", line)
      sub(/^[[:space:]]+/, "", line)
      sub(/[[:space:]]+$/, "", line)
      if (line !~ /^[A-Za-z0-9][A-Za-z0-9+_.-]*=[^[:space:]=]+$/) invalid = 1
      print line
      if ($0 !~ /\\$/) {
        state = 0
        completed++
      }
    }
    END { exit invalid || blocks != 1 || completed != 1 || state != 0 ? 1 : 0 }
  ' "$DOCKERFILE") || fail "Core APK installation is not a single closed offline transaction: $role"
  [[ "$actual_packages" == "$expected_packages" ]] || fail "Core package roots differ from the preserved transaction: $role"
}

check_offline_apk_profile cgo-builder
check_offline_apk_profile supercronic-builder
check_offline_apk_profile core-runtime
awk '
  /^FROM / { target_arch = 0 }
  /^ARG TARGETARCH$/ { target_arch = 1 }
  /^RUN --network=none --mount=from=/ {
    installs++
    if (!target_arch) invalid = 1
  }
  END { exit invalid || installs != 3 ? 1 : 0 }
' "$DOCKERFILE" || fail "Core offline APK installation does not bind the target architecture"
if sed '/^[[:space:]]*#/d' "$DOCKERFILE" |
  grep -Eq '(^|[^[:alnum:]_.-])apk(\.static)?([^[:alnum:]_./-]|$)'; then
  fail "all-in-one Dockerfile bypasses its closed offline APK inputs"
fi

assert_runtime_pin_mutation_rejected() {
  local fixture_name=$1
  local expression=$2
  local failure_message=$3
  local mutation_root="$TMP_DIR/$fixture_name"
  local mutation_test="$mutation_root/scripts/test-core-compose.test.sh"
  local mutation_log="$TMP_DIR/$fixture_name.log"

  mkdir -p "$mutation_root/scripts" "$mutation_root/deploy/allinone" "$mutation_root/deploy/build-inputs"
  cp "$0" "$mutation_test"
  cp "$SMOKE" "$mutation_root/scripts/test-core-compose.sh"
  cp "$ROOT_DIR/docker-compose.yml" "$mutation_root/docker-compose.yml"
  cp "$BUILD_INPUTS_LOCK" "$mutation_root/deploy/build-inputs/lock.json"
  sed "$expression" "$DOCKERFILE" \
    >"$mutation_root/deploy/allinone/Dockerfile"
  if CORE_COMPOSE_RUNTIME_PIN_MUTATION_CHILD=1 bash "$mutation_test" >"$mutation_log" 2>&1; then
    fail "$failure_message"
  fi
}

if [[ "${CORE_COMPOSE_RUNTIME_PIN_MUTATION_CHILD:-0}" != "1" ]]; then
  assert_runtime_pin_mutation_rejected \
    runtime-pin-substring-mutation \
    's/c-ares=/not-c-ares=/' \
    "runtime package guard accepted a longer token containing the expected pin"
  assert_runtime_pin_mutation_rejected \
    runtime-pin-comment-mutation \
    's/c-ares=/# c-ares=/' \
    "runtime package guard accepted an expected pin from a comment"
  assert_runtime_pin_mutation_rejected \
    networked-apk-install \
    's/RUN --network=none --mount=from=core-runtime-inputs/RUN --network=default --mount=from=core-runtime-inputs/' \
    "runtime package guard accepted package network access"
  assert_runtime_pin_mutation_rejected \
    networked-cgo-apk-install \
    's/RUN --network=none --mount=from=cgo-builder-inputs/RUN --network=default --mount=from=cgo-builder-inputs/' \
    "CGO package guard accepted package network access"
  assert_runtime_pin_mutation_rejected \
    networked-supercronic-apk-install \
    's/RUN --network=none --mount=from=supercronic-builder-inputs/RUN --network=default --mount=from=supercronic-builder-inputs/' \
    "supercronic package guard accepted package network access"
  assert_runtime_pin_mutation_rejected \
    wrong-supercronic-apk-profile \
    's/install.sh supercronic-builder /install.sh cgo-builder /' \
    "supercronic package guard accepted a different installation profile"
  assert_runtime_pin_mutation_rejected \
    missing-target-architecture \
    '/^ARG TARGETARCH$/d' \
    "package guard accepted an unbound installation architecture"
  assert_runtime_pin_mutation_rejected \
    online-apk-fallback \
    '/^FROM .* AS web-builder$/a\RUN apk add --no-cache curl' \
    "runtime package guard accepted an online APK fallback"
  assert_runtime_pin_mutation_rejected \
    global-options-apk-fallback \
    '/^FROM .* AS web-builder$/a\RUN apk --no-cache add curl' \
    "runtime package guard accepted APK global options before its command"
  assert_runtime_pin_mutation_rejected \
    update-cache-apk-fallback \
    '/^FROM .* AS web-builder$/a\RUN apk --update-cache add curl' \
    "runtime package guard accepted an APK cache refresh and online install"
  assert_runtime_pin_mutation_rejected \
    absolute-apk-fallback \
    '/^FROM .* AS web-builder$/a\RUN /sbin/apk --no-cache add curl' \
    "runtime package guard accepted an absolute APK command path"
  assert_runtime_pin_mutation_rejected \
    exec-form-apk-fallback \
    '/^FROM .* AS web-builder$/a\RUN ["/sbin/apk", "--no-cache", "add", "curl"]' \
    "runtime package guard accepted an exec-form APK command"
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
