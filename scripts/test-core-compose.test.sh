#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SMOKE="$ROOT_DIR/scripts/test-core-compose.sh"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

fail() {
  echo "core Compose smoke self-test: $*" >&2
  exit 1
}

if [[ ! -f "$SMOKE" ]]; then
  fail "smoke script not found: $SMOKE"
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
  *" ps -q xirang") echo core-smoke-container ;;
  "inspect --format {{.State.Health.Status}} core-smoke-container") echo healthy ;;
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
if grep -Eq -- '(^| )(system prune|container prune|rm -f|--profile asset-worker)( |$)' "$DOCKER_LOG"; then
  fail "smoke used broad cleanup or enabled the Worker profile"
fi
if ! grep -Fq -- 'http://127.0.0.1:10761/healthz' "$CURL_LOG"; then
  fail "smoke did not probe the unchanged core health endpoint"
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
