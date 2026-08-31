#!/usr/bin/env bash
# Validate the published migration version against the latest paired migration
# files. Migration filenames are the source of truth for this check.

set -euo pipefail

ROOT_DIR="${MIGRATION_FRESHNESS_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
README_PATH="${MIGRATION_FRESHNESS_README:-$ROOT_DIR/backend/README_backend.md}"
SQLITE_DIR="$ROOT_DIR/backend/internal/database/migrations/sqlite"
POSTGRES_DIR="$ROOT_DIR/backend/internal/database/migrations/postgres"

fail() {
  echo "migration documentation freshness: $*" >&2
  exit 1
}

latest_migration() {
  local dir="$1"
  local path base version name
  local best_version=""
  local best_name=""
  local -a files

  [[ -d "$dir" ]] || fail "migration directory is missing: $dir"
  shopt -s nullglob
  files=("$dir"/*.up.sql)
  shopt -u nullglob
  ((${#files[@]} > 0)) || fail "no up migrations found in: $dir"

  for path in "${files[@]}"; do
    base="${path##*/}"
    if [[ "$base" =~ ^([0-9]{6})_(.+)\.up\.sql$ ]]; then
      version="${BASH_REMATCH[1]}"
      name="${BASH_REMATCH[2]}"
      if [[ -z "$best_version" ]] || ((10#$version > 10#$best_version)); then
        best_version="$version"
        best_name="$name"
      fi
    fi
  done

  [[ -n "$best_version" ]] || fail "no versioned up migrations found in: $dir"
  printf '%s\n' "${best_version}_${best_name}"
}

require_file() {
  local path="$1"
  [[ -f "$path" && ! -L "$path" ]] || fail "paired migration file is missing: $path"
  [[ -s "$path" ]] || fail "paired migration file is empty: $path"
}

sqlite_latest="$(latest_migration "$SQLITE_DIR")"
postgres_latest="$(latest_migration "$POSTGRES_DIR")"
[[ "$sqlite_latest" == "$postgres_latest" ]] || fail "latest SQLite migration ($sqlite_latest) differs from PostgreSQL migration ($postgres_latest)"

latest_version="${sqlite_latest%%_*}"
latest_name="${sqlite_latest#*_}"
require_file "$SQLITE_DIR/${latest_version}_${latest_name}.up.sql"
require_file "$SQLITE_DIR/${latest_version}_${latest_name}.down.sql"
require_file "$POSTGRES_DIR/${latest_version}_${latest_name}.up.sql"
require_file "$POSTGRES_DIR/${latest_version}_${latest_name}.down.sql"
[[ -f "$README_PATH" ]] || fail "backend README is missing: $README_PATH"

documented="$(sed -n 's/.*当前迁移版本：`\([^`]*\)`.*$/\1/p' "$README_PATH" | awk 'NR == 1 { print; exit }')"
[[ -n "$documented" ]] || fail "backend README does not document a migration version"
[[ "$documented" == "$sqlite_latest" ]] || fail "backend README documents $documented, source migrations require $sqlite_latest"

echo "migration documentation freshness: PASS ($sqlite_latest)"
