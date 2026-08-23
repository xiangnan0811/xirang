#!/usr/bin/env bash
# scripts/check-backup-asset-migration.sh
#
# Fast schema contract for paired 000071_backup_asset_ga and
# 000072_task_run_snapshot_compatibility files plus fail-closed used-down
# admission. This checker inspects SQL/triggers and
# reuses the existing SQLite used-down owner. It does not apply unbounded
# datasets. Million-row / bomb / restart soaks stay local-only comments
# in scripts/test-backup-asset-load.sh.
#
# Usage:
#   bash scripts/check-backup-asset-migration.sh
# Exit: 0 OK; 1 contract failure; 2 missing toolchain/layout.

set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SQLITE_DIR="$ROOT_DIR/backend/internal/database/migrations/sqlite"
POSTGRES_DIR="$ROOT_DIR/backend/internal/database/migrations/postgres"
GA_VERSION=000071
GA_NAME=backup_asset_ga
COMPAT_VERSION=000072
COMPAT_NAME=task_run_snapshot_compatibility

fail() {
  echo "backup-asset migration check: $*" >&2
  exit 1
}

require_file() {
  local path=$1
  [[ -f "$path" && ! -L "$path" ]] || fail "paired migration file is missing: $path"
  [[ -s "$path" ]] || fail "paired migration file is empty: $path"
}

require_text() {
  local path=$1
  local needle=$2
  grep -Fq -- "$needle" "$path" || fail "$path is missing ${needle}"
}

if [[ ! -d "$SQLITE_DIR" || ! -d "$POSTGRES_DIR" ]]; then
  echo "backup-asset migration check: migration directories are missing" >&2
  exit 2
fi

SQLITE_UP="$SQLITE_DIR/${GA_VERSION}_${GA_NAME}.up.sql"
SQLITE_DOWN="$SQLITE_DIR/${GA_VERSION}_${GA_NAME}.down.sql"
POSTGRES_UP="$POSTGRES_DIR/${GA_VERSION}_${GA_NAME}.up.sql"
POSTGRES_DOWN="$POSTGRES_DIR/${GA_VERSION}_${GA_NAME}.down.sql"
SQLITE_COMPAT_UP="$SQLITE_DIR/${COMPAT_VERSION}_${COMPAT_NAME}.up.sql"
SQLITE_COMPAT_DOWN="$SQLITE_DIR/${COMPAT_VERSION}_${COMPAT_NAME}.down.sql"
POSTGRES_COMPAT_UP="$POSTGRES_DIR/${COMPAT_VERSION}_${COMPAT_NAME}.up.sql"
POSTGRES_COMPAT_DOWN="$POSTGRES_DIR/${COMPAT_VERSION}_${COMPAT_NAME}.down.sql"

for path in \
  "$SQLITE_UP" "$SQLITE_DOWN" "$POSTGRES_UP" "$POSTGRES_DOWN" \
  "$SQLITE_COMPAT_UP" "$SQLITE_COMPAT_DOWN" "$POSTGRES_COMPAT_UP" "$POSTGRES_COMPAT_DOWN"; do
  require_file "$path"
done

sqlite_071=$(find "$SQLITE_DIR" -maxdepth 1 -type f -name "${GA_VERSION}_*" | wc -l)
postgres_071=$(find "$POSTGRES_DIR" -maxdepth 1 -type f -name "${GA_VERSION}_*" | wc -l)
sqlite_072=$(find "$SQLITE_DIR" -maxdepth 1 -type f -name "${COMPAT_VERSION}_*" | wc -l)
postgres_072=$(find "$POSTGRES_DIR" -maxdepth 1 -type f -name "${COMPAT_VERSION}_*" | wc -l)
[[ "$sqlite_071" -eq 2 ]] || fail "SQLite must have exactly two ${GA_VERSION}_* files"
[[ "$postgres_071" -eq 2 ]] || fail "PostgreSQL must have exactly two ${GA_VERSION}_* files"
[[ "$sqlite_072" -eq 2 ]] || fail "SQLite must have exactly two ${COMPAT_VERSION}_* files"
[[ "$postgres_072" -eq 2 ]] || fail "PostgreSQL must have exactly two ${COMPAT_VERSION}_* files"

for path in "$SQLITE_UP" "$SQLITE_DOWN" "$POSTGRES_UP" "$POSTGRES_DOWN"; do
  require_text "$path" "backup_asset_installations"
  require_text "$path" "backup_asset_inventory_runs"
  require_text "$path" "backup_asset_repository_conflicts"
done

for path in "$SQLITE_UP" "$SQLITE_DOWN" "$POSTGRES_UP" "$POSTGRES_DOWN"; do
  require_text "$path" "trg_backup_asset_ga_downgrade_admission"
  require_text "$path" "ready"
  require_text "$path" "acknowledged"
  require_text "$path" "enablement_succeeded_at"
done

require_text "$SQLITE_UP" "SELECT RAISE(ABORT, '000071 downgrade blocked: backup asset GA readiness or conflict state exists');"
require_text "$SQLITE_DOWN" "CREATE TEMP TABLE backup_asset_071_down_guard"
require_text "$SQLITE_DOWN" "THEN 0 ELSE 1 END;"
require_text "$POSTGRES_UP" "CREATE OR REPLACE FUNCTION backup_asset_ga_downgrade_admission()"
require_text "$POSTGRES_UP" "RAISE EXCEPTION '000071 downgrade blocked: backup asset GA readiness or conflict state exists';"
require_text "$POSTGRES_DOWN" "RAISE EXCEPTION '000071 down blocked: backup asset GA readiness or conflict state exists';"

for path in "$SQLITE_COMPAT_UP" "$SQLITE_COMPAT_DOWN" "$POSTGRES_COMPAT_UP" "$POSTGRES_COMPAT_DOWN"; do
  require_text "$path" "node_id_snapshot"
  require_text "$path" "success"
  require_text "$path" "failed"
  require_text "$path" "canceled"
  require_text "$path" "warning"
  require_text "$path" "skipped"
  require_text "$path" "trg_backup_asset_recovery_task_runs_node_snapshot_insert"
  require_text "$path" "trg_backup_asset_recovery_task_runs_node_snapshot_immutable"
  require_text "$path" "trg_backup_asset_task_runs_legacy_unknown_status_immutable"
  require_text "$path" "trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission"
done

require_text "$SQLITE_COMPAT_UP" "SELECT RAISE(ABORT, '000072 downgrade blocked: legacy_unknown TaskRun history exists');"
require_text "$SQLITE_COMPAT_DOWN" "CREATE TEMP TABLE backup_asset_072_down_guard"
require_text "$POSTGRES_COMPAT_UP" "ADD CONSTRAINT task_runs_node_id_snapshot_compatibility"
require_text "$POSTGRES_COMPAT_DOWN" "ADD CONSTRAINT task_runs_node_id_snapshot_positive CHECK (node_id_snapshot > 0)"

if ! command -v go >/dev/null 2>&1; then
  echo "backup-asset migration check: go is required for the used-down owner" >&2
  exit 2
fi

(
  cd "$ROOT_DIR/backend"
  go test ./internal/database \
    -run '^(TestBackupAssetMigration071PairedFiles|TestBackupAssetMigration071UsedDownAdmissionSQLite|TestBackupAssetMigration072PairedFiles|TestBackupAssetMigration072SQLite)$' \
    -count=1
) || fail "000071/000072 paired-file or SQLite used-down owner failed"

echo "backup-asset migration check: PASS"
echo "backup-asset migration check: paired ${GA_VERSION}_${GA_NAME} and ${COMPAT_VERSION}_${COMPAT_NAME} files exist; used-down admission is fail-closed"
