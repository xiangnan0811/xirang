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
  printf '%s\n' '-- up' >"$dialect_dir/000077_lifecycle_effect_claim_audit_slot.up.sql"
  printf '%s\n' '-- down' >"$dialect_dir/000077_lifecycle_effect_claim_audit_slot.down.sql"
done
printf '%s\n' '当前迁移版本：`000077_lifecycle_effect_claim_audit_slot`。'>"$README"

run_checker() {
  MIGRATION_FRESHNESS_ROOT="$WORK" MIGRATION_FRESHNESS_README="$README" bash "$SCRIPT" "$@"
}

run_checker >/dev/null

printf '%s\n' '当前迁移版本：`000076_provider_native_version_reference_reason`。'>"$README"
if run_checker >/dev/null 2>&1; then
  echo 'FAIL: checker accepted stale README migration version' >&2
  exit 1
fi

printf '%s\n' '当前迁移版本：`000077_lifecycle_effect_claim_audit_slot`。'>"$README"
rm "$POSTGRES_DIR/000077_lifecycle_effect_claim_audit_slot.down.sql"
if run_checker >/dev/null 2>&1; then
  echo 'FAIL: checker accepted an unpaired latest migration' >&2
  exit 1
fi

for dialect_dir in "$SQLITE_DIR" "$POSTGRES_DIR"; do
  printf '%s\n' '-- up' >"$dialect_dir/000078_duplicate_a.up.sql"
  printf '%s\n' '-- up' >"$dialect_dir/000078_duplicate_b.up.sql"
  printf '%s\n' '-- down' >"$dialect_dir/000078_duplicate_a.down.sql"
  printf '%s\n' '-- down' >"$dialect_dir/000078_duplicate_b.down.sql"
done
if run_checker >/dev/null 2>&1; then
  echo 'FAIL: checker accepted multiple latest migration names' >&2
  exit 1
fi

echo 'migration freshness checker self-test: PASS'
