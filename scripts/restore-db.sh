#!/usr/bin/env bash
set -euo pipefail

if ! umask 077; then
  echo "❌ 无法设置数据库恢复私有 umask" >&2
  exit 1
fi

usage() {
  cat <<'USAGE'
用法：
  restore-db.sh <backup_file>

说明：
  - DB_TYPE=sqlite（默认）：离线恢复到 SQLITE_PATH（默认 ./backend/xirang.db）。
    恢复前必须停止 Xirang（包括 Compose 可选服务），并设置
    XIRANG_RESTORE_OFFLINE=1；脚本会拒绝检测到的运行中 Xirang 服务。
  - DB_TYPE=postgres：
      *.dump 通过 pg_restore 恢复
      *.sql  通过 psql 恢复

环境变量：
  DB_TYPE=sqlite|postgres
  SQLITE_PATH=./backend/xirang.db
  DB_DSN=postgresql://user:pass@host:5432/dbname?sslmode=disable
  XIRANG_RESTORE_OFFLINE=1  # SQLite 离线恢复的显式维护窗口确认
USAGE
}

fail() {
  echo "❌ $*" >&2
  exit 1
}

require_mode() {
  local path="$1"
  local expected="$2"
  local actual
  if ! actual="$(stat -c '%a' -- "${path}")"; then
    fail "无法确认私有文件权限：${path}"
  fi
  [[ "${actual}" == "${expected}" ]] || fail "私有文件权限不符合要求：${path}"
}

db_type="${DB_TYPE:-sqlite}"
backup_file="${1:-}"

if [[ -z "${backup_file}" ]]; then
  usage
  exit 1
fi

if [[ ! -f "${backup_file}" || ! -s "${backup_file}" ]]; then
  fail "备份文件不存在或为空：${backup_file}"
fi

checksum_tool=""
if command -v sha256sum >/dev/null 2>&1; then
  checksum_tool="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  checksum_tool="shasum"
fi

calculate_checksum() {
  local path="$1"
  case "${checksum_tool}" in
    sha256sum) sha256sum -- "${path}" | awk '{print $1}' ;;
    shasum) shasum -a 256 -- "${path}" | awk '{print $1}' ;;
    *) fail "未找到 sha256sum 或 shasum，无法验证备份完整性" ;;
  esac
}

verify_checksum() {
  local path="$1"
  local checksum_file="${path}.sha256"
  local expected actual
  if [[ ! -s "${checksum_file}" ]]; then
    fail "未找到有效校验文件（${checksum_file}），拒绝恢复"
  fi
  expected="$(awk 'NF {print $1; exit}' "${checksum_file}")"
  if [[ ! "${expected}" =~ ^[[:xdigit:]]{64}$ ]]; then
    fail "校验文件格式无效：${checksum_file}"
  fi
  actual="$(calculate_checksum "${path}")"
  if [[ "${actual}" != "${expected}" ]]; then
    fail "备份文件校验失败，拒绝恢复：${path}"
  fi
  echo "✅ 备份校验通过：${path}"
}

is_xirang_service_running() {
  local proc pid cmdline state containers container_name

  # The /proc check works both on the host and inside the All-in-One image,
  # without requiring procps or a process-name override that an operator could
  # accidentally use to bypass the offline guard.
  if [[ -d /proc ]]; then
    for proc in /proc/[0-9]*; do
      pid="${proc##*/}"
      [[ "${pid}" == "$$" ]] && continue
      [[ -r "${proc}/cmdline" ]] || continue
      cmdline="$(tr '\0' ' ' < "${proc}/cmdline" 2>/dev/null || true)"
      case "${cmdline}" in
        /usr/local/bin/xirang|/usr/local/bin/xirang\ *|\
        */xirang|*/xirang\ *|xirang|xirang\ *)
          return 0
          ;;
      esac
    done
  fi

  # Host-side Compose deployments may not expose the container process in the
  # host shell's /proc namespace. Inspect the fixed Compose container when the
  # Docker CLI is available. Return 2 when Docker is present but cannot prove
  # that the container is stopped or absent, so callers can fail closed.
  if command -v docker >/dev/null 2>&1; then
    if state="$(docker inspect --format='{{.State.Running}}' xirang 2>/dev/null)"; then
      case "${state}" in
        true) return 0 ;;
        false) return 1 ;;
        *) return 2 ;;
      esac
    fi

    # `docker inspect` uses the same non-zero status for a missing container and
    # control-plane failures. A successful all-container query can prove true
    # absence; any query failure or an existing container with unknown state is
    # ambiguous and must block the restore.
    if ! containers="$(docker ps --all --format '{{.Names}}' 2>/dev/null)"; then
      return 2
    fi
    while IFS= read -r container_name; do
      [[ "${container_name}" == "xirang" ]] && return 2
    done <<<"${containers}"
  fi
  return 1
}

if [[ "${db_type}" == "sqlite" ]]; then
  if [[ "${XIRANG_RESTORE_OFFLINE:-}" != "1" ]]; then
    fail "SQLite 恢复仅允许离线执行；停止 Xirang 后设置 XIRANG_RESTORE_OFFLINE=1"
  fi
  if is_xirang_service_running; then
    fail "检测到运行中的 Xirang 服务，拒绝覆盖 SQLite；请先停止服务"
  else
    service_state_status=$?
    if [[ "${service_state_status}" -ne 1 ]]; then
      fail "无法确认 Xirang Compose 容器状态，拒绝覆盖 SQLite；请检查 Docker 服务和访问权限"
    fi
  fi
  if ! command -v sqlite3 >/dev/null 2>&1; then
    fail "未找到 sqlite3，无法验证 SQLite 备份"
  fi

  sqlite_path="${SQLITE_PATH:-./backend/xirang.db}"
  sqlite_dir="$(dirname -- "${sqlite_path}")"
  mkdir -p -- "${sqlite_dir}"
  if ! chmod 0700 -- "${sqlite_dir}"; then
    fail "无法收紧 SQLite 数据目录权限：${sqlite_dir}"
  fi
  require_mode "${sqlite_dir}" 700
  verify_checksum "${backup_file}"

  integrity=""
  if ! integrity="$(sqlite3 "${backup_file}" 'PRAGMA integrity_check;' 2>/dev/null)"; then
    fail "SQLite 备份无法打开，拒绝恢复：${backup_file}"
  fi
  if [[ "${integrity}" != "ok" ]]; then
    fail "SQLite 备份完整性检查失败，拒绝恢复：${backup_file}"
  fi

  rollback_file=""
  if [[ -f "${sqlite_path}" ]]; then
    rollback_file="$(mktemp "${sqlite_path}.before-restore.XXXXXX.bak")"
    if ! chmod 0600 -- "${rollback_file}"; then
      fail "无法收紧恢复前 SQLite 回滚副本权限：${rollback_file}"
    fi
    if ! sqlite3 "${sqlite_path}" ".timeout 5000" ".backup '${rollback_file}'"; then
      fail "无法创建恢复前 SQLite 回滚副本：${rollback_file}"
    fi
    [[ -s "${rollback_file}" ]] || fail "恢复前 SQLite 回滚副本为空：${rollback_file}"
    if ! chmod 0600 -- "${rollback_file}"; then
      fail "无法确认恢复前 SQLite 回滚副本私有权限：${rollback_file}"
    fi
    require_mode "${rollback_file}" 600
    echo "🔁 已备份当前 SQLite 文件：${rollback_file}"
  fi

  restore_tmp="$(mktemp "${sqlite_path}.restore.XXXXXX")"
  if ! chmod 0600 -- "${restore_tmp}"; then
    rm -f -- "${restore_tmp}" 2>/dev/null || true
    fail "无法收紧 SQLite 临时恢复文件权限：${restore_tmp}"
  fi
  cleanup_restore() {
    local status=$?
    [[ -n "${restore_tmp:-}" ]] && rm -f -- "${restore_tmp}" 2>/dev/null || true
    return "${status}"
  }
  trap cleanup_restore EXIT
  cp -- "${backup_file}" "${restore_tmp}"
  [[ -s "${restore_tmp}" ]] || fail "临时恢复文件为空：${restore_tmp}"
  if ! chmod 0600 -- "${restore_tmp}"; then
    fail "无法确认 SQLite 临时恢复文件私有权限：${restore_tmp}"
  fi
  require_mode "${restore_tmp}" 600

  # mktemp creates the temporary file next to the target, so rename is an
  # atomic publication on the same filesystem. The live WAL sidecars are
  # removed only after the offline guard and successful publication.
  mv -f -- "${restore_tmp}" "${sqlite_path}"
  restore_tmp=""
  # Rename preserves the already-verified temporary-file mode; only verify at
  # the live publication boundary so no post-publication chmod can fail.
  require_mode "${sqlite_path}" 600
  if ! rm -f -- "${sqlite_path}-wal" "${sqlite_path}-shm"; then
    fail "无法删除 SQLite WAL/SHM sidecar，拒绝报告恢复成功"
  fi
  if ! integrity="$(sqlite3 "${sqlite_path}" 'PRAGMA integrity_check;' 2>/dev/null)"; then
    fail "恢复后的 SQLite 数据库无法打开：${sqlite_path}"
  fi
  [[ "${integrity}" == "ok" ]] || fail "恢复后的 SQLite 完整性检查失败：${sqlite_path}"
  echo "✅ SQLite 离线恢复完成并通过完整性检查：${sqlite_path}"
  exit 0
fi

if [[ "${db_type}" == "postgres" ]]; then
  dsn="${DB_DSN:-}"
  [[ -n "${dsn}" ]] || fail "DB_TYPE=postgres 时必须设置 DB_DSN"

  verify_checksum "${backup_file}"

  if [[ "${backup_file}" == *.dump ]]; then
    command -v pg_restore >/dev/null 2>&1 || fail "未找到 pg_restore，请先安装 PostgreSQL 客户端工具"
    pg_restore --single-transaction --clean --if-exists --no-owner --no-privileges \
      --dbname "${dsn}" "${backup_file}"
    echo "✅ PostgreSQL（custom dump）恢复完成：${backup_file}"
    exit 0
  fi

  if [[ "${backup_file}" == *.sql ]]; then
    command -v psql >/dev/null 2>&1 || fail "未找到 psql，请先安装 PostgreSQL 客户端工具"
    psql --single-transaction --set ON_ERROR_STOP=on --dbname "${dsn}" --file "${backup_file}"
    echo "✅ PostgreSQL（SQL）恢复完成：${backup_file}"
    exit 0
  fi

  fail "PostgreSQL 仅支持 .dump 或 .sql 备份文件"
fi

fail "不支持的 DB_TYPE：${db_type}（仅支持 sqlite / postgres）"
