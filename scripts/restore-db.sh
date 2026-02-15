#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
用法：
  restore-db.sh <backup_file>

说明：
  - DB_TYPE=sqlite（默认）：将备份文件恢复到 SQLITE_PATH（默认 ./backend/xirang.db）
  - DB_TYPE=postgres：
      *.dump 通过 pg_restore 恢复
      *.sql  通过 psql 恢复

环境变量：
  DB_TYPE=sqlite|postgres
  SQLITE_PATH=./backend/xirang.db
  DB_DSN=postgresql://user:pass@host:5432/dbname?sslmode=disable
USAGE
}

db_type="${DB_TYPE:-sqlite}"
backup_file="${1:-}"

if [[ -z "${backup_file}" ]]; then
  usage
  exit 1
fi

if [[ ! -f "${backup_file}" ]]; then
  echo "❌ 备份文件不存在：${backup_file}" >&2
  exit 1
fi

if [[ "${db_type}" == "sqlite" ]]; then
  sqlite_path="${SQLITE_PATH:-./backend/xirang.db}"
  mkdir -p "$(dirname "${sqlite_path}")"

  if [[ -f "${sqlite_path}" ]]; then
    rollback_file="${sqlite_path}.before-restore.$(date +%Y%m%d-%H%M%S).bak"
    cp "${sqlite_path}" "${rollback_file}"
    echo "🔁 已备份当前 SQLite 文件：${rollback_file}"
  fi

  cp "${backup_file}" "${sqlite_path}"
  echo "✅ SQLite 恢复完成：${sqlite_path}"
  exit 0
fi

if [[ "${db_type}" == "postgres" ]]; then
  dsn="${DB_DSN:-}"
  if [[ -z "${dsn}" ]]; then
    echo "❌ DB_TYPE=postgres 时必须设置 DB_DSN" >&2
    exit 1
  fi

  if [[ "${backup_file}" == *.dump ]]; then
    if ! command -v pg_restore >/dev/null 2>&1; then
      echo "❌ 未找到 pg_restore，请先安装 PostgreSQL 客户端工具" >&2
      exit 1
    fi

    pg_restore --clean --if-exists --no-owner --no-privileges --dbname "${dsn}" "${backup_file}"
    echo "✅ PostgreSQL（custom dump）恢复完成：${backup_file}"
    exit 0
  fi

  if [[ "${backup_file}" == *.sql ]]; then
    if ! command -v psql >/dev/null 2>&1; then
      echo "❌ 未找到 psql，请先安装 PostgreSQL 客户端工具" >&2
      exit 1
    fi

    psql "${dsn}" -f "${backup_file}"
    echo "✅ PostgreSQL（SQL）恢复完成：${backup_file}"
    exit 0
  fi

  echo "❌ PostgreSQL 仅支持 .dump 或 .sql 备份文件" >&2
  exit 1
fi

echo "❌ 不支持的 DB_TYPE：${db_type}（仅支持 sqlite / postgres）" >&2
exit 1
