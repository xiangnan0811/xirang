#!/usr/bin/env bash
# scripts/check-backup-asset-migration.sh
#
# Fast schema contract for paired 000071_backup_asset_ga,
# 000072_task_run_snapshot_compatibility, 000073_backup_asset_plain_text_content,
# and 000077_lifecycle_effect_claim_audit_slot files plus their fail-closed
# used-down admission. This checker inspects SQL/triggers and reuses the
# existing SQLite used-down owners. It does not apply unbounded datasets.
# Million-row / bomb / restart soaks stay local-only comments in
# scripts/test-backup-asset-load.sh.
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
PLAIN_TEXT_VERSION=000073
PLAIN_TEXT_NAME=backup_asset_plain_text_content
LIFECYCLE_EFFECT_VERSION=000077
LIFECYCLE_EFFECT_NAME=lifecycle_effect_claim_audit_slot

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
SQLITE_PLAIN_TEXT_UP="$SQLITE_DIR/${PLAIN_TEXT_VERSION}_${PLAIN_TEXT_NAME}.up.sql"
SQLITE_PLAIN_TEXT_DOWN="$SQLITE_DIR/${PLAIN_TEXT_VERSION}_${PLAIN_TEXT_NAME}.down.sql"
POSTGRES_PLAIN_TEXT_UP="$POSTGRES_DIR/${PLAIN_TEXT_VERSION}_${PLAIN_TEXT_NAME}.up.sql"
POSTGRES_PLAIN_TEXT_DOWN="$POSTGRES_DIR/${PLAIN_TEXT_VERSION}_${PLAIN_TEXT_NAME}.down.sql"
SQLITE_LIFECYCLE_EFFECT_UP="$SQLITE_DIR/${LIFECYCLE_EFFECT_VERSION}_${LIFECYCLE_EFFECT_NAME}.up.sql"
SQLITE_LIFECYCLE_EFFECT_DOWN="$SQLITE_DIR/${LIFECYCLE_EFFECT_VERSION}_${LIFECYCLE_EFFECT_NAME}.down.sql"
POSTGRES_LIFECYCLE_EFFECT_UP="$POSTGRES_DIR/${LIFECYCLE_EFFECT_VERSION}_${LIFECYCLE_EFFECT_NAME}.up.sql"
POSTGRES_LIFECYCLE_EFFECT_DOWN="$POSTGRES_DIR/${LIFECYCLE_EFFECT_VERSION}_${LIFECYCLE_EFFECT_NAME}.down.sql"

for path in \
  "$SQLITE_UP" "$SQLITE_DOWN" "$POSTGRES_UP" "$POSTGRES_DOWN" \
  "$SQLITE_COMPAT_UP" "$SQLITE_COMPAT_DOWN" "$POSTGRES_COMPAT_UP" "$POSTGRES_COMPAT_DOWN" \
  "$SQLITE_PLAIN_TEXT_UP" "$SQLITE_PLAIN_TEXT_DOWN" \
  "$POSTGRES_PLAIN_TEXT_UP" "$POSTGRES_PLAIN_TEXT_DOWN" \
  "$SQLITE_LIFECYCLE_EFFECT_UP" "$SQLITE_LIFECYCLE_EFFECT_DOWN" \
  "$POSTGRES_LIFECYCLE_EFFECT_UP" "$POSTGRES_LIFECYCLE_EFFECT_DOWN"; do
  require_file "$path"
done

sqlite_071=$(find "$SQLITE_DIR" -maxdepth 1 -type f -name "${GA_VERSION}_*" | wc -l)
postgres_071=$(find "$POSTGRES_DIR" -maxdepth 1 -type f -name "${GA_VERSION}_*" | wc -l)
sqlite_072=$(find "$SQLITE_DIR" -maxdepth 1 -type f -name "${COMPAT_VERSION}_*" | wc -l)
postgres_072=$(find "$POSTGRES_DIR" -maxdepth 1 -type f -name "${COMPAT_VERSION}_*" | wc -l)
sqlite_073=$(find "$SQLITE_DIR" -maxdepth 1 -type f -name "${PLAIN_TEXT_VERSION}_*" | wc -l)
postgres_073=$(find "$POSTGRES_DIR" -maxdepth 1 -type f -name "${PLAIN_TEXT_VERSION}_*" | wc -l)
sqlite_077=$(find "$SQLITE_DIR" -maxdepth 1 -type f -name "${LIFECYCLE_EFFECT_VERSION}_*" | wc -l)
postgres_077=$(find "$POSTGRES_DIR" -maxdepth 1 -type f -name "${LIFECYCLE_EFFECT_VERSION}_*" | wc -l)
[[ "$sqlite_077" -eq 2 ]] || fail "SQLite must have exactly two ${LIFECYCLE_EFFECT_VERSION}_* files"
[[ "$postgres_077" -eq 2 ]] || fail "PostgreSQL must have exactly two ${LIFECYCLE_EFFECT_VERSION}_* files"
[[ "$sqlite_071" -eq 2 ]] || fail "SQLite must have exactly two ${GA_VERSION}_* files"
[[ "$postgres_071" -eq 2 ]] || fail "PostgreSQL must have exactly two ${GA_VERSION}_* files"
[[ "$sqlite_072" -eq 2 ]] || fail "SQLite must have exactly two ${COMPAT_VERSION}_* files"
[[ "$postgres_072" -eq 2 ]] || fail "PostgreSQL must have exactly two ${COMPAT_VERSION}_* files"
[[ "$sqlite_073" -eq 2 ]] || fail "SQLite must have exactly two ${PLAIN_TEXT_VERSION}_* files"
[[ "$postgres_073" -eq 2 ]] || fail "PostgreSQL must have exactly two ${PLAIN_TEXT_VERSION}_* files"

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

for path in "$SQLITE_PLAIN_TEXT_UP" "$SQLITE_PLAIN_TEXT_DOWN" "$POSTGRES_PLAIN_TEXT_UP" "$POSTGRES_PLAIN_TEXT_DOWN"; do
  require_text "$path" "backup_asset_delivery_grants"
  require_text "$path" "plain_text"
  require_text "$path" "text_v2"
  require_text "$path" "trg_backup_asset_plain_text_content_downgrade_admission"
done

for path in "$SQLITE_PLAIN_TEXT_UP" "$SQLITE_PLAIN_TEXT_DOWN"; do
  require_text "$path" "CREATE TEMP TABLE backup_asset_delivery_requests_000073_hold"
  require_text "$path" "idx_backup_asset_delivery_grants_delivery_state"
  require_text "$path" "idx_backup_asset_delivery_requests_grant_state"
  require_text "$path" "trg_backup_asset_recovery_content_authorization_insert"
  require_text "$path" "trg_backup_asset_recovery_content_authorization_update"
  require_text "$path" "trg_backup_asset_recovery_content_binding_immutable"
  require_text "$path" "trg_backup_asset_recovery_downgrade_admission"
done

require_text "$SQLITE_PLAIN_TEXT_UP" "renderer = 'plain_text' AND profile = 'text_v2' AND range_policy = 'none'"
require_text "$SQLITE_PLAIN_TEXT_DOWN" "WHERE renderer = 'plain_text' OR profile = 'text_v2'"
for path in "$POSTGRES_PLAIN_TEXT_UP" "$POSTGRES_PLAIN_TEXT_DOWN"; do
  require_text "$path" "backup_asset_delivery_grants_renderer_check"
  require_text "$path" "backup_asset_delivery_grants_profile_check"
  require_text "$path" "backup_asset_delivery_grants_renderer_product_check"
  require_text "$path" "backup_asset_delivery_grants_representation_product_check"
done
require_text "$POSTGRES_PLAIN_TEXT_UP" "CREATE OR REPLACE FUNCTION backup_asset_plain_text_content_downgrade_admission()"
require_text "$POSTGRES_PLAIN_TEXT_DOWN" "RAISE EXCEPTION '000073 down blocked: plain_text/text_v2 delivery grant exists';"

for path in \
  "$SQLITE_LIFECYCLE_EFFECT_UP" "$SQLITE_LIFECYCLE_EFFECT_DOWN" \
  "$POSTGRES_LIFECYCLE_EFFECT_UP" "$POSTGRES_LIFECYCLE_EFFECT_DOWN"; do
  require_text "$path" "recovery_point_lifecycle_effect_claims"
  require_text "$path" "recovery_point_lifecycle_audit_slots"
  require_text "$path" "idx_recovery_point_lifecycle_effect_claims_attempt"
  require_text "$path" "idx_recovery_point_lifecycle_audit_slots_attempt_status"
  require_text "$path" "trg_recovery_point_lifecycle_effect_claims_transition"
  require_text "$path" "trg_recovery_point_lifecycle_effect_claims_no_delete"
  require_text "$path" "trg_recovery_point_lifecycle_audit_slots_transition"
  require_text "$path" "trg_recovery_point_lifecycle_audit_slots_immutable_update"
  require_text "$path" "trg_recovery_point_lifecycle_audit_slots_immutable_delete"
  require_text "$path" "trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission"
done

for path in "$SQLITE_LIFECYCLE_EFFECT_UP" "$POSTGRES_LIFECYCLE_EFFECT_UP"; do
  require_text "$path" "lifecycle_effect_audit_slot_000077_candidates"
  require_text "$path" "tombstone_valid"
  require_text "$path" "first_event_at"
  require_text "$path" "last_event_at"
  require_text "$path" "lifecycle_effect_audit_slot_000077_events"
  require_text "$path" "lifecycle_effect_audit_slot_000077_matches"
  require_text "$path" "repository_purge"
  require_text "$path" "repository_id"
  require_text "$path" "recovery_point_id"
  require_text "$path" "terminal_state"
  require_text "$path" "purged_at"
  require_text "$path" "deletion_receipt_digest"
  require_text "$path" "outcome"
  require_text "$path" "fields_json"
  require_text "$path" "item_count"
  require_text "$path" "stage"
  require_text "$path" "settled"
  require_text "$path" "source"
  require_text "$path" "retention_expire"
  require_text "$path" "explicit_purge"
  require_text "$path" "active_hold"
  require_text "$path" "provider_worm"
  require_text "$path" "provider_unavailable"
  require_text "$path" "provider_identity_conflict"
  require_text "$path" "provider_native_version_referenced"
  require_text "$path" "provider_delete_unproven"
  require_text "$path" "deletion_unavailable"
  require_text "$path" "in_flight"
  require_text "$path" "uncertain"
  require_text "$path" "proven"
  require_text "$path" "deleted"
  require_text "$path" "already_absent"
  require_text "$path" "identity_conflict"
done
require_text "$SQLITE_LIFECYCLE_EFFECT_UP" "lifecycle_effect_claim_audit_slot_000077_backfill_guard"
require_text "$POSTGRES_LIFECYCLE_EFFECT_UP" "lifecycle_effect_claim_audit_slot_000077_backfill_guard"

require_text "$SQLITE_LIFECYCLE_EFFECT_UP" "lifecycle_effect_claim_audit_slot_000077_cutover_guard"
require_text "$POSTGRES_LIFECYCLE_EFFECT_UP" "000077 upgrade requires quiesced provider_delete attempts with valid receipts"
require_text "$SQLITE_LIFECYCLE_EFFECT_DOWN" "lifecycle_effect_claims_000077_down_guard"
require_text "$SQLITE_LIFECYCLE_EFFECT_DOWN" "lifecycle_effect_audit_slots_000077_down_guard"
require_text "$POSTGRES_LIFECYCLE_EFFECT_DOWN" "000077 downgrade blocked: lifecycle effect claim exists"
require_text "$POSTGRES_LIFECYCLE_EFFECT_DOWN" "000077 downgrade blocked: lifecycle audit slot exists"

bash "$ROOT_DIR/scripts/check-migration-version.sh" || fail "backend README migration version is stale"
if ! command -v go >/dev/null 2>&1; then
  echo "backup-asset migration check: go is required for the used-down owner" >&2
  exit 2
fi

(
  cd "$ROOT_DIR/backend"
  go test ./internal/database \
    -run '^(TestBackupAssetMigration071PairedFiles|TestBackupAssetMigration071UsedDownAdmissionSQLite|TestBackupAssetMigration072PairedFiles|TestBackupAssetMigration072SQLite|TestBackupAssetMigration073PairedFiles|TestBackupAssetMigration073SQLite|TestRunMigrationsClean072Applies073SQLite|TestLifecycleEffectClaimAuditSlotMigrationSQLite(PristineDown|ClaimUsedDown|SlotUsedDown|Constraints|ClaimTransitionRebinding|UpgradeCutover))$' \
    -count=1
) || fail "paired-file, freshness, or SQLite used-down owner failed"
echo "backup-asset migration check: PASS"
echo "backup-asset migration check: paired ${GA_VERSION}_${GA_NAME}, ${COMPAT_VERSION}_${COMPAT_NAME}, ${PLAIN_TEXT_VERSION}_${PLAIN_TEXT_NAME}, and ${LIFECYCLE_EFFECT_VERSION}_${LIFECYCLE_EFFECT_NAME} files exist; used-down admission is fail-closed"
