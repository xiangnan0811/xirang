#!/usr/bin/env bash
set -Eeuo pipefail

# Start one disposable OpenSSH server for the real-browser lifecycle, then the
# disposable backend that must trust exactly that host key. Strict host-key
# checking stays enabled. The runtime must be the empty directory just created
# for this run. Cleanup is armed only after that check, so a refusal cannot
# delete an occupied or unrelated scratch directory.
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
SCRATCH="${ROOT_DIR}/.tmp/agent"
RUNTIME_DIR="${E2E_RUNTIME_DIR:-}"
SAME_RUN="${E2E_RUNTIME_SAME_RUN:-}"
SCRATCH_REAL=""
OWN_RUNTIME=0
SSHD_PID=""
BACKEND_PID=""

refuse() {
  echo "$1" >&2
  exit 1
}

runtime_still_safe() {
  [[ "${SAME_RUN}" =~ ^[0-9a-f]{32}$ ]] || return 1
  [[ "${RUNTIME_DIR}" == /* ]] || return 1
  [[ "$(basename -- "${RUNTIME_DIR}")" =~ ^runtime\.[A-Za-z0-9]+$ ]] || return 1
  local parent grand grand_real
  parent="$(dirname -- "${RUNTIME_DIR}")"
  grand="$(dirname -- "${parent}")"
  [[ "$(basename -- "${parent}")" == "lifecycle-p1-e2e" ]] || return 1
  grand_real="$(realpath -e "${grand}" 2>/dev/null)" || return 1
  [[ -n "${SCRATCH_REAL}" && "${grand_real}" == "${SCRATCH_REAL}" ]] || return 1
  local current="${RUNTIME_DIR}"
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

runtime_is_empty() {
  [[ "${RUNTIME_DIR}" != *[*?[]* ]] || return 1
  local saved_dotglob=0 saved_nullglob=0
  shopt -q dotglob && saved_dotglob=1
  shopt -q nullglob && saved_nullglob=1
  shopt -s dotglob nullglob
  local -a entries=("${RUNTIME_DIR}"/*)
  if (( saved_dotglob == 0 )); then shopt -u dotglob; fi
  if (( saved_nullglob == 0 )); then shopt -u nullglob; fi
  (( ${#entries[@]} == 0 ))
}

stop_sshd() {
  if [[ -z "${SSHD_PID}" ]]; then
    return
  fi
  kill -TERM -"${SSHD_PID}" 2>/dev/null || kill -TERM "${SSHD_PID}" 2>/dev/null || true
  local _
  for _ in {1..20}; do
    if ! kill -0 "${SSHD_PID}" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done
  kill -KILL -"${SSHD_PID}" 2>/dev/null || kill -KILL "${SSHD_PID}" 2>/dev/null || true
  wait "${SSHD_PID}" 2>/dev/null || true
  SSHD_PID=""
}

remove_runtime() {
  if [[ "${OWN_RUNTIME}" != "1" ]]; then
    return
  fi
  if [[ -L "${RUNTIME_DIR}" || ! -d "${RUNTIME_DIR}" ]]; then
    echo "refusing to remove a runtime that is no longer a real directory" >&2
    return
  fi
  if ! runtime_still_safe; then
    echo "refusing to remove a runtime outside this run" >&2
    return
  fi
  rm -rf -- "${RUNTIME_DIR}"
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "${BACKEND_PID}" ]] && kill -0 "${BACKEND_PID}" 2>/dev/null; then
    kill -TERM "${BACKEND_PID}" 2>/dev/null || true
    local _
    for _ in {1..50}; do
      if ! kill -0 "${BACKEND_PID}" 2>/dev/null; then
        break
      fi
      sleep 0.1
    done
    kill -KILL "${BACKEND_PID}" 2>/dev/null || true
    wait "${BACKEND_PID}" 2>/dev/null || true
  fi
  BACKEND_PID=""
  stop_sshd
  remove_runtime
  exit "${status}"
}
if [[ -L "${ROOT_DIR}/.tmp" || -L "${SCRATCH}" ]]; then
  refuse "refusing a symlinked .tmp/agent scratch path"
fi
if [[ ! -d "${SCRATCH}" ]]; then
  refuse ".tmp/agent is not a directory"
fi
if ! SCRATCH_REAL="$(realpath -e "${SCRATCH}")"; then
  refuse ".tmp/agent could not be resolved"
fi
if [[ ! "${SAME_RUN}" =~ ^[0-9a-f]{32}$ ]]; then
  refuse "refusing a runtime that is not marked for this run"
fi
if [[ -z "${RUNTIME_DIR}" || "${RUNTIME_DIR}" != /* ]]; then
  refuse "E2E_RUNTIME_DIR must be an absolute directory created for this run"
fi
if [[ -L "${RUNTIME_DIR}" ]]; then
  refuse "refusing a symlinked runtime directory"
fi
if ! runtime_still_safe; then
  refuse "refusing a runtime outside the fresh lifecycle directory"
fi
if ! runtime_is_empty; then
  refuse "refusing an occupied runtime directory"
fi

OWN_RUNTIME=1
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

umask 077
SSH_DIR="${RUNTIME_DIR}/ssh"
mkdir -m 700 "${SSH_DIR}"
USER_KEY="${SSH_DIR}/user_ed25519"
HOST_KEY="${SSH_DIR}/host_ed25519"
AUTHORIZED_KEYS="${SSH_DIR}/authorized_keys"
SSHD_CONFIG="${SSH_DIR}/sshd_config"
KNOWN_HOSTS="${SSH_DIR}/known_hosts"
SSHD_LOG="${SSH_DIR}/sshd.log"
SQLITE_PATH="${RUNTIME_DIR}/xirang.db"

SSH_USER="$(id -un)"
if [[ ! "${SSH_USER}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]]; then
  echo "current user is not a usable ssh username" >&2
  exit 1
fi
PERMIT_ROOT="no"
if [[ "${SSH_USER}" == "root" ]]; then
  PERMIT_ROOT="yes"
fi

SSHD_BIN=""
if command -v sshd >/dev/null 2>&1; then
  SSHD_BIN="$(command -v sshd)"
else
  for candidate in /usr/sbin/sshd /usr/local/sbin/sshd; do
    if [[ -x "${candidate}" ]]; then
      SSHD_BIN="${candidate}"
      break
    fi
  done
fi
if [[ -z "${SSHD_BIN}" ]]; then
  echo "sshd is not installed" >&2
  exit 1
fi

ssh-keygen -q -t ed25519 -N "" -C "xirang-e2e-user" -f "${USER_KEY}"
ssh-keygen -q -t ed25519 -N "" -C "xirang-e2e-host" -f "${HOST_KEY}"
chmod 600 "${USER_KEY}" "${HOST_KEY}"
cp "${USER_KEY}.pub" "${AUTHORIZED_KEYS}"
chmod 600 "${AUTHORIZED_KEYS}"
HOST_PUBLIC="$(awk 'NR==1 {print $1, $2}' "${HOST_KEY}.pub")"
if [[ ! "${HOST_PUBLIC}" =~ ^ssh-ed25519\ [A-Za-z0-9+/]+$ ]]; then
  echo "isolated host public key was not produced" >&2
  exit 1
fi

INCLUDE_CHALLENGE=1
write_sshd_config() {
  local port="$1"
  {
    printf 'Port %s\n' "${port}"
    printf 'ListenAddress 127.0.0.1\n'
    printf 'AddressFamily inet\n'
    printf 'HostKey %s\n' "${HOST_KEY}"
    printf 'AuthorizedKeysFile %s\n' "${AUTHORIZED_KEYS}"
    printf 'PasswordAuthentication no\n'
    printf 'KbdInteractiveAuthentication no\n'
    if (( INCLUDE_CHALLENGE == 1 )); then
      printf 'ChallengeResponseAuthentication no\n'
    fi
    printf 'UsePAM no\n'
    printf 'PermitRootLogin %s\n' "${PERMIT_ROOT}"
    printf 'PubkeyAuthentication yes\n'
    printf 'AuthenticationMethods publickey\n'
    printf 'StrictModes no\n'
    printf 'UseDNS no\n'
    printf 'PrintMotd no\n'
    printf 'PidFile %s\n' "${SSH_DIR}/sshd.pid"
    printf 'PermitUserEnvironment no\n'
    printf 'AllowTcpForwarding no\n'
    printf 'AllowAgentForwarding no\n'
    printf 'X11Forwarding no\n'
    printf 'PermitTunnel no\n'
    printf 'AllowUsers %s\n' "${SSH_USER}"
    printf 'LogLevel INFO\n'
  } > "${SSHD_CONFIG}"
  chmod 600 "${SSHD_CONFIG}"
}

accept_sshd_config() {
  if "${SSHD_BIN}" -t -f "${SSHD_CONFIG}" >/dev/null 2>"${SSH_DIR}/sshd-test.err"; then
    return 0
  fi
  if (( INCLUDE_CHALLENGE == 1 )) && grep -Eq 'ChallengeResponseAuthentication|Unsupported option|Bad configuration option' "${SSH_DIR}/sshd-test.err"; then
    INCLUDE_CHALLENGE=0
    write_sshd_config "${SSH_PORT}"
    if "${SSHD_BIN}" -t -f "${SSHD_CONFIG}" >/dev/null 2>"${SSH_DIR}/sshd-test.err"; then
      return 0
    fi
  fi
  echo "isolated sshd config was rejected" >&2
  cat "${SSH_DIR}/sshd-test.err" >&2 || true
  return 1
}

started=0
SSH_PORT=""
for _attempt in 1 2 3 4 5; do
  stop_sshd
  SSH_PORT="$(python3 -c 'import socket; probe = socket.socket(); probe.bind(("127.0.0.1", 0)); print(probe.getsockname()[1]); probe.close()')"
  if [[ ! "${SSH_PORT}" =~ ^[1-9][0-9]*$ || "${SSH_PORT}" -gt 65535 ]]; then
    echo "isolated sshd port was not allocated" >&2
    exit 1
  fi
  write_sshd_config "${SSH_PORT}"
  accept_sshd_config
  : > "${SSHD_LOG}"
  if command -v setsid >/dev/null 2>&1; then
    setsid "${SSHD_BIN}" -D -e -f "${SSHD_CONFIG}" >"${SSHD_LOG}" 2>&1 &
  else
    "${SSHD_BIN}" -D -e -f "${SSHD_CONFIG}" >"${SSHD_LOG}" 2>&1 &
  fi
  SSHD_PID=$!
  ready=0
  deadline=$((SECONDS + 5))
  while (( SECONDS < deadline )); do
    if { echo >/dev/tcp/127.0.0.1/"${SSH_PORT}"; } >/dev/null 2>&1; then
      ready=1
      break
    fi
    if ! kill -0 "${SSHD_PID}" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done
  if (( ready == 0 )); then
    continue
  fi
  printf '[127.0.0.1]:%s %s\n' "${SSH_PORT}" "${HOST_PUBLIC}" > "${KNOWN_HOSTS}"
  chmod 600 "${KNOWN_HOSTS}"
  recorded="$(ssh-keygen -F "[127.0.0.1]:${SSH_PORT}" -f "${KNOWN_HOSTS}" | awk '/^\[127\.0\.0\.1\]/{print $2, $3}')" || true
  if [[ "${recorded}" != "${HOST_PUBLIC}" ]]; then
    echo "isolated known_hosts does not match the generated host key" >&2
    exit 1
  fi
  ssh_cmd=(
    ssh
    -o BatchMode=yes
    -o StrictHostKeyChecking=yes
    -o CheckHostIP=yes
    -o HashKnownHosts=no
    -o UpdateHostKeys=no
    -o UserKnownHostsFile="${KNOWN_HOSTS}"
    -o GlobalKnownHostsFile=/dev/null
    -o IdentitiesOnly=yes
    -o PreferredAuthentications=publickey
    -o PasswordAuthentication=no
    -o KbdInteractiveAuthentication=no
    -o ConnectTimeout=5
    -o RequestTTY=force
    -i "${USER_KEY}"
    -p "${SSH_PORT}"
    "${SSH_USER}@127.0.0.1"
    true
  )
  if command -v timeout >/dev/null 2>&1; then
    if timeout 10 "${ssh_cmd[@]}"; then
      started=1
      break
    fi
  elif "${ssh_cmd[@]}"; then
    started=1
    break
  fi
done
if (( started == 0 )); then
  echo "isolated sshd did not accept a strict known_hosts login" >&2
  cat "${SSHD_LOG}" >&2 || true
  exit 1
fi

python3 - "${RUNTIME_DIR}/fixture.json" "${SSH_PORT}" "${USER_KEY}" "${KNOWN_HOSTS}" "${SQLITE_PATH}" "${SSH_USER}" <<'PY'
import json
import os
import sys

destination, port_text, key_path, known_hosts, sqlite_path, username = sys.argv[1:]
payload = {
    "port": int(port_text),
    "privateKeyPath": key_path,
    "knownHostsPath": known_hosts,
    "sqlitePath": sqlite_path,
    "username": username,
}
temporary = destination + ".tmp"
with open(temporary, "w", encoding="utf-8") as handle:
    json.dump(payload, handle)
    handle.write("\n")
os.chmod(temporary, 0o600)
os.replace(temporary, destination)
PY

umask 022

bash "${ROOT_DIR}/scripts/e2e-backend.sh" &
BACKEND_PID=$!
set +e
wait "${BACKEND_PID}"
status=$?
set -e
BACKEND_PID=""
exit "${status}"
