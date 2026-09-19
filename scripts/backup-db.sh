#!/usr/bin/env bash
set -euo pipefail

if ! umask 077; then
  echo "❌ 无法设置数据库备份私有 umask" >&2
  exit 1
fi

db_type="${DB_TYPE:-sqlite}"
timestamp="$(date +%Y%m%d-%H%M%S)"
output_dir="${1:-./backups}"

mkdir -p -- "${output_dir}"
if [[ ! -d "${output_dir}" ]]; then
  echo "❌ 备份目录不是目录：${output_dir}" >&2
  exit 1
fi
if ! chmod 0700 -- "${output_dir}"; then
  echo "❌ 无法收紧备份目录权限：${output_dir}" >&2
  exit 1
fi

require_mode() {
  local path="$1"
  local expected="$2"
  local actual
  if ! actual="$(stat -c '%a' -- "${path}")"; then
    echo "❌ 无法确认私有权限：${path}" >&2
    return 1
  fi
  if [[ "${actual}" != "${expected}" ]]; then
    echo "❌ 私有权限不符合要求：${path}（期望 ${expected}，实际 ${actual}）" >&2
    return 1
  fi
}

require_mode "${output_dir}" 700

# A killed backup (SIGKILL, OOM, container restart) cannot run its EXIT trap, so
# its `*.tmp.<pid>` artifacts, the matching SQLite sidecars, and the checksum
# temporary are left behind forever and can silently consume the whole volume.
# Remove only what is provably orphaned: the owning pid is gone, or the file is
# older than a day. Never touch this invocation's own temporary names.
pid_is_running() {
  local pid="$1"
  [[ -n "${pid}" ]] || return 1
  # /proc also covers pids owned by another user, where kill -0 may report EPERM.
  [[ -d "/proc/${pid}" ]] && return 0
  kill -0 "${pid}" 2>/dev/null
}

artifact_is_older_than_a_day() {
  local path="$1"
  [[ -n "$(find "${path}" -maxdepth 0 -mmin +1440 -print -quit 2>/dev/null)" ]]
}

purge_orphan_temp_artifacts() {
  local dir="$1"
  local path base suffix pid
  shopt -s nullglob
  for path in \
    "${dir}"/xirang-sqlite-*.db.tmp.* \
    "${dir}"/xirang-postgres-*.dump.tmp.* \
    "${dir}"/*.sha256.tmp.* \
    "${dir}"/*.tmp.*-journal \
    "${dir}"/*.tmp.*-wal \
    "${dir}"/*.tmp.*-shm; do
    [[ -f "${path}" ]] || continue
    base="$(basename -- "${path}")"
    suffix="${base##*.tmp.}"
    pid="${suffix%%-*}"
    if [[ "${pid}" == "$$" ]]; then
      continue
    fi
    if pid_is_running "${pid}"; then
      continue
    fi
    if [[ "${pid}" =~ ^[0-9]+$ ]] || artifact_is_older_than_a_day "${path}"; then
      if ! rm -f -- "${path}"; then
        echo "⚠️  无法清理孤儿备份临时文件：${path}" >&2
      fi
    fi
  done
  shopt -u nullglob
}

purge_orphan_temp_artifacts "${output_dir}"

# Refuse to start a SQLite snapshot when the destination filesystem cannot hold
# the source database plus a fixed reserve. `sqlite3 .backup` and the integrity
# check both need headroom, and failing mid-copy is what produces the orphaned
# temporary files this script otherwise cleans up.
require_sqlite_backup_space() {
  local source="$1"
  local dir="$2"
  local source_bytes available_kib required_bytes available_bytes
  if ! source_bytes="$(stat -c '%s' -- "${source}")"; then
    echo "❌ 无法读取 SQLite 源库大小：${source}" >&2
    return 1
  fi
  if ! available_kib="$(df -Pk -- "${dir}" | awk 'NR==2 {print $4}')"; then
    echo "❌ 无法读取备份目录可用空间：${dir}" >&2
    return 1
  fi
  if [[ ! "${available_kib}" =~ ^[0-9]+$ ]]; then
    echo "❌ 备份目录可用空间无法解析：${dir}" >&2
    return 1
  fi
  required_bytes=$(( source_bytes + 64 * 1024 * 1024 ))
  available_bytes=$(( available_kib * 1024 ))
  if (( available_bytes < required_bytes )); then
    echo "❌ 备份目录可用空间不足：${dir} 可用 ${available_bytes} 字节，至少需要 ${required_bytes} 字节（源库 ${source_bytes} + 64MiB）" >&2
    return 1
  fi
}

if command -v sha256sum >/dev/null 2>&1; then
  checksum_tool="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  checksum_tool="shasum"
else
  echo "❌ 未找到 sha256sum 或 shasum，无法生成备份校验文件" >&2
  exit 1
fi

backup_file=""
checksum_file=""
artifact_tmp=""
checksum_tmp=""
published_artifact=0
published_checksum=0
cleanup() {
  local status=$?
  if [[ "${status}" -ne 0 ]]; then
    [[ "${published_artifact}" -eq 1 ]] && [[ -n "${backup_file}" ]] && rm -f -- "${backup_file}"
    [[ "${published_checksum}" -eq 1 ]] && [[ -n "${checksum_file}" ]] && rm -f -- "${checksum_file}"
  fi
  [[ -n "${artifact_tmp}" ]] && rm -f -- "${artifact_tmp}"
  [[ -n "${checksum_tmp}" ]] && rm -f -- "${checksum_tmp}"
  return "${status}"
}
trap cleanup EXIT

calculate_checksum() {
  local path="$1"
  if [[ "${checksum_tool}" == "sha256sum" ]]; then
    sha256sum -- "${path}" | awk '{print $1}'
  else
    shasum -a 256 -- "${path}" | awk '{print $1}'
  fi
}

finish_backup() {
  local extension="$1"
  local label="PostgreSQL"
  [[ "${db_type}" == "sqlite" ]] && label="SQLite"
  backup_file="${output_dir}/xirang-${db_type}-${timestamp}.${extension}"
  checksum_file="${backup_file}.sha256"
  if [[ -e "${backup_file}" || -e "${checksum_file}" ]]; then
    echo "❌ 备份目标已存在，拒绝覆盖：${backup_file}" >&2
    return 1
  fi

  artifact_tmp="${backup_file}.tmp.$$"
  checksum_tmp="${checksum_file}.tmp.$$"
  if [[ ! -s "${artifact_tmp}" ]]; then
    echo "❌ 备份产物为空：${artifact_tmp}" >&2
    return 1
  fi
  if ! chmod 0600 -- "${artifact_tmp}"; then
    echo "❌ 无法收紧备份临时文件权限：${artifact_tmp}" >&2
    return 1
  fi
  require_mode "${artifact_tmp}" 600

  local digest
  digest="$(calculate_checksum "${artifact_tmp}")"
  if [[ ! "${digest}" =~ ^[[:xdigit:]]{64}$ ]]; then
    echo "❌ 无法生成有效 SHA-256 校验值：${artifact_tmp}" >&2
    return 1
  fi
  printf '%s  %s\n' "${digest}" "${backup_file}" > "${checksum_tmp}"
  if [[ ! -s "${checksum_tmp}" ]]; then
    echo "❌ 校验文件为空：${checksum_tmp}" >&2
    return 1
  fi
  if ! chmod 0600 -- "${checksum_tmp}"; then
    echo "❌ 无法收紧校验临时文件权限：${checksum_tmp}" >&2
    return 1
  fi
  require_mode "${checksum_tmp}" 600
  mv -- "${artifact_tmp}" "${backup_file}"
  artifact_tmp=""
  published_artifact=1
  if ! chmod 0600 -- "${backup_file}"; then
    echo "❌ 无法确认备份产物私有权限：${backup_file}" >&2
    return 1
  fi
  require_mode "${backup_file}" 600
  mv -- "${checksum_tmp}" "${checksum_file}"
  checksum_tmp=""
  published_checksum=1
  if ! chmod 0600 -- "${checksum_file}"; then
    echo "❌ 无法确认校验文件私有权限：${checksum_file}" >&2
    return 1
  fi
  require_mode "${checksum_file}" 600

  if [[ ! -s "${backup_file}" || ! -s "${checksum_file}" ]]; then
    echo "❌ 备份产物或校验文件缺失：${backup_file}" >&2
    return 1
  fi
  if [[ "$(calculate_checksum "${backup_file}")" != "${digest}" ]]; then
    echo "❌ 备份产物校验失败：${backup_file}" >&2
    return 1
  fi
  echo "✅ ${label} 备份完成：${backup_file}"
  echo "BACKUP_FILE=${backup_file}"
}

if [[ "${db_type}" == "sqlite" ]]; then
  sqlite_path="${SQLITE_PATH:-./backend/xirang.db}"
  if [[ ! -f "${sqlite_path}" ]]; then
    echo "❌ SQLite 文件不存在：${sqlite_path}" >&2
    exit 1
  fi
  if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "❌ 未找到 sqlite3，无法执行一致性 SQLite 备份" >&2
    exit 1
  fi

  backup_file="${output_dir}/xirang-sqlite-${timestamp}.db"
  checksum_file="${backup_file}.sha256"
  if [[ -e "${backup_file}" || -e "${checksum_file}" ]]; then
    echo "❌ 备份目标已存在，拒绝覆盖：${backup_file}" >&2
    exit 1
  fi
  artifact_tmp="${backup_file}.tmp.$$"
  require_sqlite_backup_space "${sqlite_path}" "${output_dir}"
  sqlite3 "${sqlite_path}" ".timeout 5000" ".backup '${artifact_tmp}'"
  if [[ ! -s "${artifact_tmp}" ]]; then
    echo "❌ SQLite 备份产物为空：${artifact_tmp}" >&2
    exit 1
  fi
  if [[ "$(sqlite3 "${artifact_tmp}" 'PRAGMA integrity_check;')" != "ok" ]]; then
    echo "❌ SQLite 备份完整性检查失败：${artifact_tmp}" >&2
    exit 1
  fi
  finish_backup "db"
  exit 0
fi

if [[ "${db_type}" == "postgres" ]]; then
  dsn="${DB_DSN:-}"
  if [[ -z "${dsn}" ]]; then
    echo "❌ DB_TYPE=postgres 时必须设置 DB_DSN" >&2
    exit 1
  fi
  if ! command -v pg_dump >/dev/null 2>&1; then
    echo "❌ 未找到 pg_dump，请先安装 PostgreSQL 客户端工具" >&2
    exit 1
  fi

  backup_file="${output_dir}/xirang-postgres-${timestamp}.dump"
  checksum_file="${backup_file}.sha256"
  if [[ -e "${backup_file}" || -e "${checksum_file}" ]]; then
    echo "❌ 备份目标已存在，拒绝覆盖：${backup_file}" >&2
    exit 1
  fi
  artifact_tmp="${backup_file}.tmp.$$"
  pg_dump "${dsn}" --format=custom --file "${artifact_tmp}" --no-owner --no-privileges
  if [[ ! -s "${artifact_tmp}" ]]; then
    echo "❌ PostgreSQL 备份产物为空：${artifact_tmp}" >&2
    exit 1
  fi
  finish_backup "dump"
  exit 0
fi

echo "❌ 不支持的 DB_TYPE：${db_type}（仅支持 sqlite / postgres）" >&2
exit 1
