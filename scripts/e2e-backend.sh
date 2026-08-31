#!/usr/bin/env bash
set -Eeuo pipefail

# Start a disposable backend for the real-browser smoke. The database and
# compiled binary both live below a temporary directory so this harness cannot
# leave application data in the checkout.
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="${ROOT_DIR}/backend"
BACKEND_PORT="${E2E_BACKEND_PORT:-18080}"
FRONTEND_PORT="${E2E_VITE_PORT:-4178}"
ADMIN_PASSWORD="${E2E_ADMIN_PASSWORD:-FAKE_E2E_AdminPass2026!_FOR_TEST_ONLY}"

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/xirang-playwright-backend.XXXXXX")"
SERVER_LOG="${TEMP_DIR}/server.log"
SERVER_BINARY="${TEMP_DIR}/xirang-server"
SERVER_PID=""

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill -TERM "${SERVER_PID}" 2>/dev/null || true
    for _ in {1..50}; do
      if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
        break
      fi
      sleep 0.1
    done
    kill -KILL "${SERVER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SERVER_PID}" ]]; then
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${TEMP_DIR}"
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

(
  cd "${BACKEND_DIR}"
  go build -o "${SERVER_BINARY}" ./cmd/server
)

(
  cd "${BACKEND_DIR}"
  exec env \
    APP_ENV=development \
    ENVIRONMENT=development \
    GIN_MODE=release \
    SERVER_ADDR="127.0.0.1:${BACKEND_PORT}" \
    DB_TYPE=sqlite \
    SQLITE_PATH="${TEMP_DIR}/xirang.db" \
    ADMIN_INITIAL_PASSWORD="${ADMIN_PASSWORD}" \
    JWT_SECRET=FAKE_E2E_JWT_SECRET_2026_FOR_TEST_ONLY_LONG \
    CORS_ALLOWED_ORIGINS="http://127.0.0.1:${FRONTEND_PORT}" \
    TRUSTED_PROXIES=127.0.0.1 \
    LOG_LEVEL=warn \
    "${SERVER_BINARY}"
) >"${SERVER_LOG}" 2>&1 &
SERVER_PID=$!

ready=0
deadline=$((SECONDS + 120))
while (( SECONDS < deadline )); do
  if curl --fail --silent --show-error "http://127.0.0.1:${BACKEND_PORT}/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
    break
  fi
  sleep 0.2
done

if (( ready == 0 )); then
  echo "real-browser backend did not become ready" >&2
  cat "${SERVER_LOG}" >&2 || true
  exit 1
fi

if wait "${SERVER_PID}"; then
  exit 0
else
  status=$?
  cat "${SERVER_LOG}" >&2 || true
  exit "${status}"
fi
