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

SERVER_PID=""
DELETE_TEMP=0
TEMP_DIR=""
RUNTIME_DIR=""
SCRATCH_REAL=""

refuse() {
  echo "$1" >&2
  exit 1
}

runtime_still_safe() {
  local candidate="${1:-}"
  [[ "${candidate}" == /* ]] || return 1
  [[ "$(basename -- "${candidate}")" =~ ^runtime\.[A-Za-z0-9]+$ ]] || return 1
  local parent grand grand_real
  parent="$(dirname -- "${candidate}")"
  grand="$(dirname -- "${parent}")"
  [[ "$(basename -- "${parent}")" == "lifecycle-p1-e2e" ]] || return 1
  grand_real="$(realpath -e "${grand}" 2>/dev/null)" || return 1
  [[ -n "${SCRATCH_REAL}" && "${grand_real}" == "${SCRATCH_REAL}" ]] || return 1
  local current="${candidate}"
  local reached=0
  while [[ "${current}" != "/" ]]; do
    if [[ -L "${current}" ]]; then
      echo "refusing symlink path component: ${current}" >&2
      return 1
    fi
    [[ -d "${current}" ]] || return 1
    local current_real
    current_real="$(realpath -e "${current}" 2>/dev/null)" || return 1
    if [[ "${current_real}" == "${SCRATCH_REAL}" ]]; then
      reached=1
      break
    fi
    local next
    next="$(dirname -- "${current}")"
    [[ "${next}" == "${current}" ]] && break
    current="${next}"
  done
  (( reached == 1 ))
}

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
  if (( DELETE_TEMP == 1 )); then
    rm -rf -- "${TEMP_DIR}"
  fi
  exit "${status}"
}

arm_cleanup() {
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
}

SAME_RUN="${E2E_RUNTIME_SAME_RUN:-}"
RUNTIME_INPUT="${E2E_RUNTIME_DIR:-}"
if [[ -n "${SAME_RUN}" || -n "${RUNTIME_INPUT}" ]]; then
  if [[ ! "${SAME_RUN}" =~ ^[0-9a-f]{32}$ ]]; then
    refuse "refusing an inherited runtime that is not marked for this run"
  fi
  SCRATCH="${ROOT_DIR}/.tmp/agent"
  if [[ -L "${ROOT_DIR}/.tmp" || -L "${SCRATCH}" || ! -d "${SCRATCH}" ]]; then
    refuse "refusing a symlinked or missing .tmp/agent scratch path"
  fi
  if ! SCRATCH_REAL="$(realpath -e "${SCRATCH}")"; then
    refuse ".tmp/agent could not be resolved"
  fi
  if ! runtime_still_safe "${RUNTIME_INPUT}"; then
    refuse "refusing a runtime outside the fresh lifecycle directory"
  fi
  RUNTIME_DIR="${RUNTIME_INPUT}"
  TEMP_DIR="${RUNTIME_DIR}"
  KNOWN_HOSTS="${TEMP_DIR}/ssh/known_hosts"
  if [[ -L "${TEMP_DIR}/ssh" || -L "${KNOWN_HOSTS}" || ! -f "${KNOWN_HOSTS}" ]]; then
    refuse "isolated known_hosts is not a regular file inside the runtime"
  fi
  arm_cleanup
else
  TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/xirang-playwright-backend.XXXXXX")"
  DELETE_TEMP=1
  KNOWN_HOSTS="${TEMP_DIR}/known_hosts"
  arm_cleanup
fi

SERVER_LOG="${TEMP_DIR}/server.log"
SERVER_BINARY="${TEMP_DIR}/xirang-server"
CRON_DIR="${TEMP_DIR}/cron-backups"
TEST_SQLITE_PATH="${TEMP_DIR}/xirang.db"
if [[ -L "${TEST_SQLITE_PATH}" ]]; then
  refuse "refusing a symlinked database path"
fi
if [[ -e "${TEST_SQLITE_PATH}" ]]; then
  refuse "refusing to reuse an existing real-browser fixture database"
fi
if [[ -L "${CRON_DIR}" || ( -e "${CRON_DIR}" && ! -d "${CRON_DIR}" ) ]]; then
  refuse "cron backup directory must be a real directory path"
fi
mkdir -p -m 700 "${CRON_DIR}"
if [[ -L "${CRON_DIR}" || ! -d "${CRON_DIR}" ]]; then
  refuse "cron backup directory was not created"
fi

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
    SQLITE_PATH="${TEST_SQLITE_PATH}" \
    DB_BACKUP_DIR="${TEMP_DIR}/web-backups" \
    ADMIN_INITIAL_PASSWORD="${ADMIN_PASSWORD}" \
    JWT_SECRET=FAKE_E2E_JWT_SECRET_2026_FOR_TEST_ONLY_LONG \
    DATA_ENCRYPTION_KEY=FAKE_E2E_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY \
    CRON_DB_BACKUP_DIR="${CRON_DIR}" \
    SSH_STRICT_HOST_KEY_CHECKING=true \
    SSH_AUTO_ACCEPT_NEW_HOSTS=false \
    SSH_KNOWN_HOSTS_PATH="${KNOWN_HOSTS}" \
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
