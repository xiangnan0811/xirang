#!/usr/bin/env bash
# Self-test for scripts/check-migration-version.sh.

set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/check-migration-version.sh"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT

SQLITE_DIR="$WORK/backend/internal/database/migrations/sqlite"
POSTGRES_DIR="$WORK/backend/internal/database/migrations/postgres"
README="$WORK/backend/README_backend.md"
mkdir -p "$SQLITE_DIR" "$POSTGRES_DIR" "$(dirname "$README")"

for dialect_dir in "$SQLITE_DIR" "$POSTGRES_DIR"; do
  printf '%s\n' '-- up' >"$dialect_dir/000073_backup_asset_plain_text_content.up.sql"
  printf '%s\n' '-- down' >"$dialect_dir/000073_backup_asset_plain_text_content.down.sql"
done
printf '%s\n' '当前迁移版本：`000073_backup_asset_plain_text_content`。'>"$README"

run_checker() {
  MIGRATION_FRESHNESS_ROOT="$WORK" MIGRATION_FRESHNESS_README="$README" bash "$SCRIPT" "$@"
}

run_checker >/dev/null

printf '%s\n' '当前迁移版本：`000072_task_run_snapshot_compatibility`。'>"$README"
if run_checker >/dev/null 2>&1; then
  echo 'FAIL: checker accepted stale README migration version' >&2
  exit 1
fi

printf '%s\n' '当前迁移版本：`000073_backup_asset_plain_text_content`。'>"$README"
rm "$POSTGRES_DIR/000073_backup_asset_plain_text_content.down.sql"
if run_checker >/dev/null 2>&1; then
  echo 'FAIL: checker accepted an unpaired latest migration' >&2
  exit 1
fi

echo 'migration freshness checker self-test: PASS'
