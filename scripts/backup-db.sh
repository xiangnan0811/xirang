#!/usr/bin/env bash
set -euo pipefail

db_type="${DB_TYPE:-sqlite}"
timestamp="$(date +%Y%m%d-%H%M%S)"
output_dir="${1:-./backups}"

mkdir -p "${output_dir}"

if [[ "${db_type}" == "sqlite" ]]; then
  sqlite_path="${SQLITE_PATH:-./backend/xirang.db}"
  if [[ ! -f "${sqlite_path}" ]]; then
    echo "❌ SQLite 文件不存在：${sqlite_path}" >&2
    exit 1
  fi

  backup_file="${output_dir}/xirang-sqlite-${timestamp}.db"
  cp "${sqlite_path}" "${backup_file}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${backup_file}" > "${backup_file}.sha256"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${backup_file}" > "${backup_file}.sha256"
  fi

  echo "✅ SQLite 备份完成：${backup_file}"
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
  pg_dump "${dsn}" --format=custom --file "${backup_file}" --no-owner --no-privileges
  echo "✅ PostgreSQL 备份完成：${backup_file}"
  exit 0
fi

echo "❌ 不支持的 DB_TYPE：${db_type}（仅支持 sqlite / postgres）" >&2
exit 1

