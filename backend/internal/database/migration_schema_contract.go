package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	minimumRecoverySchemaVersion               int64 = 69
	taskRunCompatibilitySchemaVersion          int64 = 72
	plainTextContentSchemaVersion              int64 = 73
	drillDurableRecoverySchemaVersion          int64 = 74
	lifecycleEffectClaimAuditSlotSchemaVersion int64 = 77
)

const lifecycleEffectClaimAuditSlotAdmissionTrigger = "trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission"
const lifecycleEffectClaimAuditSlotAdmissionFunction = "recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission"
const lifecycleEffectClaimAuditSlotClaimsTable = "recovery_point_lifecycle_effect_claims"
const lifecycleEffectClaimAuditSlotSlotsTable = "recovery_point_lifecycle_audit_slots"

type lifecycleEffectClaimAuditSlotTriggerContract struct {
	table                                 string
	name                                  string
	invalidReason                         string
	triggerFragments                      []string
	sqliteFragmentMinimumCounts           map[string]int
	sqliteFragments                       []string
	postgresFunctionFragmentMinimumCounts map[string]int
	postgresFunctionFragments             []string
	sqliteWhen                            string
	sqliteBody                            string
	postgresFunctionBody                  string
	postgresFunctionName                  string
}

const (
	lifecycleClaimTransitionSQLiteWhen = `
		OLD.state = 'proven'
		OR NEW.id IS NOT OLD.id
		OR NEW.attempt_id IS NOT OLD.attempt_id
		OR NEW.target_identity_digest IS NOT OLD.target_identity_digest
		OR NEW.created_at IS NOT OLD.created_at
		OR (OLD.state = 'in_flight' AND NEW.state NOT IN ('in_flight', 'uncertain', 'proven'))
		OR (OLD.state = 'uncertain' AND NEW.state NOT IN ('uncertain', 'in_flight'))
		OR (OLD.state = 'uncertain' AND NEW.state = 'uncertain')
		OR (OLD.state = 'in_flight' AND NEW.state = OLD.state
			AND (NEW.executor_id IS NOT OLD.executor_id
				OR NEW.execution_id IS NOT OLD.execution_id
				OR NEW.transition_revision IS NOT OLD.transition_revision
				OR NEW.lease_id IS NOT OLD.lease_id
				OR NEW.lease_attempt_id IS NOT OLD.lease_attempt_id
				OR NEW.lease_fence_token_hash IS NOT OLD.lease_fence_token_hash))
		OR (OLD.state = 'in_flight' AND NEW.state IN ('uncertain', 'proven')
			AND (NEW.executor_id IS NOT OLD.executor_id
				OR NEW.execution_id IS NOT OLD.execution_id
				OR NEW.transition_revision IS NOT OLD.transition_revision
				OR NEW.lease_id IS NOT OLD.lease_id
				OR NEW.lease_attempt_id IS NOT OLD.lease_attempt_id
				OR NEW.lease_fence_token_hash IS NOT OLD.lease_fence_token_hash))
		OR (OLD.state = 'uncertain' AND NEW.state = 'in_flight'
			AND NEW.execution_id IS OLD.execution_id)`
	lifecycleAuditSlotTransitionSQLiteWhen = `
		EXISTS (
			SELECT 1
			FROM recovery_point_lifecycle_audit_slots
			WHERE attempt_id = NEW.attempt_id
				AND status IN ('deleted', 'already_absent')
		)`
	lifecycleAdmissionSQLiteWhen = `
		NEW.version < 77
		AND (
			EXISTS (SELECT 1 FROM recovery_point_lifecycle_effect_claims)
			OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_audit_slots)
		)`

	lifecycleClaimTransitionPostgresBody = `
		BEGIN
			IF OLD.state = 'proven' THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim is proven and immutable';
			END IF;
			IF NEW.id IS DISTINCT FROM OLD.id
			   OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
			   OR NEW.target_identity_digest IS DISTINCT FROM OLD.target_identity_digest
			   OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim identity is immutable';
			END IF;
			IF OLD.state = 'in_flight' AND NEW.state NOT IN ('in_flight', 'uncertain', 'proven') THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim state transition is invalid';
			END IF;
			IF OLD.state = 'uncertain' AND NEW.state NOT IN ('uncertain', 'in_flight') THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim takeover transition is invalid';
			END IF;
			IF OLD.state = 'uncertain' AND NEW.state = 'uncertain' THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim uncertainty is historical';
			END IF;
			IF OLD.state = 'in_flight' AND NEW.state = OLD.state
			   AND (NEW.executor_id IS DISTINCT FROM OLD.executor_id
				 OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
				 OR NEW.transition_revision IS DISTINCT FROM OLD.transition_revision
				 OR NEW.lease_id IS DISTINCT FROM OLD.lease_id
				 OR NEW.lease_attempt_id IS DISTINCT FROM OLD.lease_attempt_id
				 OR NEW.lease_fence_token_hash IS DISTINCT FROM OLD.lease_fence_token_hash) THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim renewal rebinding is invalid';
			END IF;
			IF OLD.state = 'in_flight' AND NEW.state IN ('uncertain', 'proven')
			   AND (NEW.executor_id IS DISTINCT FROM OLD.executor_id
				 OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
				 OR NEW.transition_revision IS DISTINCT FROM OLD.transition_revision
				 OR NEW.lease_id IS DISTINCT FROM OLD.lease_id
				 OR NEW.lease_attempt_id IS DISTINCT FROM OLD.lease_attempt_id
				 OR NEW.lease_fence_token_hash IS DISTINCT FROM OLD.lease_fence_token_hash) THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim binding changed before takeover';
			END IF;
			IF OLD.state = 'uncertain' AND NEW.state = 'in_flight'
			   AND NEW.execution_id IS NOT DISTINCT FROM OLD.execution_id THEN
				RAISE EXCEPTION 'recovery point lifecycle effect claim takeover must rotate execution_id';
			END IF;
			RETURN NEW;
		END;`
	lifecycleClaimDeletePostgresBody = `
		BEGIN
			RAISE EXCEPTION 'recovery point lifecycle effect claim is permanent';
		END;`
	lifecycleAuditSlotTransitionPostgresBody = `
		BEGIN
			PERFORM 1
			FROM recovery_point_lifecycle_attempts
			WHERE id = NEW.attempt_id
			FOR UPDATE;
			IF NOT FOUND THEN
				RAISE EXCEPTION 'recovery point lifecycle audit slot attempt is missing';
			END IF;
			IF EXISTS (
				SELECT 1
				FROM recovery_point_lifecycle_audit_slots
				WHERE attempt_id = NEW.attempt_id
				  AND status IN ('deleted', 'already_absent')
			) THEN
				RAISE EXCEPTION 'recovery point lifecycle audit slot follows a terminal status';
			END IF;
			RETURN NEW;
		END;`
	lifecycleAuditSlotImmutablePostgresBody = `
		BEGIN
			RAISE EXCEPTION 'recovery point lifecycle audit slot is immutable';
		END;`
	lifecycleAdmissionPostgresBody = `
		BEGIN
			IF NEW.version < 77 AND (
				EXISTS (SELECT 1 FROM recovery_point_lifecycle_effect_claims)
				OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_audit_slots)
			) THEN
				RAISE EXCEPTION '000077 downgrade blocked: lifecycle effect claim or audit slot exists';
			END IF;
			RETURN NEW;
		END;`

	lifecycleClaimTransitionSQLiteBody     = `SELECT RAISE(ABORT, 'recovery point lifecycle effect claim transition is immutable or invalid');`
	lifecycleClaimDeleteSQLiteBody         = `SELECT RAISE(ABORT, 'recovery point lifecycle effect claim is permanent');`
	lifecycleAuditSlotTransitionSQLiteBody = `SELECT RAISE(ABORT, 'recovery point lifecycle audit slot follows a terminal status');`
	lifecycleAuditSlotImmutableSQLiteBody  = `SELECT RAISE(ABORT, 'recovery point lifecycle audit slot is immutable');`
	lifecycleAuditSlotPermanentSQLiteBody  = `SELECT RAISE(ABORT, 'recovery point lifecycle audit slot is permanent');`
	lifecycleAdmissionSQLiteBody           = `SELECT RAISE(ABORT, '000077 downgrade blocked: lifecycle effect claim or audit slot exists');`
)

// Keep the v77 guard contract declarative.  The migration is deliberately
// additive, so startup must verify not just that named objects exist, but that
// each object still carries the complete fail-closed predicate after an
// operator-side restore or table rebuild.
var lifecycleEffectClaimAuditSlotTriggerContracts = []lifecycleEffectClaimAuditSlotTriggerContract{
	{
		table:                lifecycleEffectClaimAuditSlotClaimsTable,
		name:                 "trg_recovery_point_lifecycle_effect_claims_transition",
		invalidReason:        "invalid_lifecycle_effect_claim_transition_trigger",
		triggerFragments:     []string{"before update", lifecycleEffectClaimAuditSlotClaimsTable},
		sqliteWhen:           lifecycleClaimTransitionSQLiteWhen,
		sqliteBody:           lifecycleClaimTransitionSQLiteBody,
		postgresFunctionBody: lifecycleClaimTransitionPostgresBody,
		postgresFunctionName: "recovery_point_lifecycle_effect_claim_transition_guard",
		sqliteFragmentMinimumCounts: map[string]int{
			"new.executor_id is not old.executor_id":                       2,
			"new.execution_id is not old.execution_id":                     2,
			"new.transition_revision is not old.transition_revision":       2,
			"new.lease_id is not old.lease_id":                             2,
			"new.lease_attempt_id is not old.lease_attempt_id":             2,
			"new.lease_fence_token_hash is not old.lease_fence_token_hash": 2,
		},
		sqliteFragments: []string{
			"when old.state = 'proven'",
			"new.id is not old.id",
			"new.attempt_id is not old.attempt_id",
			"new.target_identity_digest is not old.target_identity_digest",
			"new.created_at is not old.created_at",
			"old.state = 'in_flight' and new.state not in ('in_flight', 'uncertain', 'proven')",
			"old.state = 'uncertain' and new.state not in ('uncertain', 'in_flight')",
			"old.state = 'uncertain' and new.state = 'uncertain'",
			"old.state = 'in_flight' and new.state = old.state",
			"new.executor_id is not old.executor_id",
			"new.execution_id is not old.execution_id",
			"new.transition_revision is not old.transition_revision",
			"new.lease_id is not old.lease_id",
			"new.lease_attempt_id is not old.lease_attempt_id",
			"new.lease_fence_token_hash is not old.lease_fence_token_hash",
			"old.state = 'in_flight' and new.state in ('uncertain', 'proven')",
			"old.state = 'uncertain' and new.state = 'in_flight'",
			"new.execution_id is old.execution_id",
			"select raise(abort",
		},
		postgresFunctionFragmentMinimumCounts: map[string]int{
			"new.executor_id is distinct from old.executor_id":                       2,
			"new.execution_id is distinct from old.execution_id":                     2,
			"new.transition_revision is distinct from old.transition_revision":       2,
			"new.lease_id is distinct from old.lease_id":                             2,
			"new.lease_attempt_id is distinct from old.lease_attempt_id":             2,
			"new.lease_fence_token_hash is distinct from old.lease_fence_token_hash": 2,
		},
		postgresFunctionFragments: []string{
			"if old.state = 'proven' then",
			"new.id is distinct from old.id",
			"new.attempt_id is distinct from old.attempt_id",
			"new.target_identity_digest is distinct from old.target_identity_digest",
			"new.created_at is distinct from old.created_at",
			"old.state = 'in_flight' and new.state not in ('in_flight', 'uncertain', 'proven')",
			"old.state = 'uncertain' and new.state = 'uncertain'",
			"old.state = 'in_flight' and new.state = old.state",
			"new.executor_id is distinct from old.executor_id",
			"new.execution_id is distinct from old.execution_id",
			"new.transition_revision is distinct from old.transition_revision",
			"new.lease_id is distinct from old.lease_id",
			"new.lease_attempt_id is distinct from old.lease_attempt_id",
			"new.lease_fence_token_hash is distinct from old.lease_fence_token_hash",
			"old.state = 'in_flight' and new.state in ('uncertain', 'proven')",
			"old.state = 'uncertain' and new.state = 'in_flight'",
			"new.execution_id is not distinct from old.execution_id",
			"raise exception",
			"return new",
		},
	},
	{
		table:                     lifecycleEffectClaimAuditSlotClaimsTable,
		name:                      "trg_recovery_point_lifecycle_effect_claims_no_delete",
		invalidReason:             "invalid_lifecycle_effect_claim_delete_trigger",
		triggerFragments:          []string{"before delete", lifecycleEffectClaimAuditSlotClaimsTable},
		sqliteBody:                lifecycleClaimDeleteSQLiteBody,
		postgresFunctionBody:      lifecycleClaimDeletePostgresBody,
		postgresFunctionName:      "recovery_point_lifecycle_effect_claim_delete_guard",
		sqliteFragments:           []string{"select raise(abort", "permanent"},
		postgresFunctionFragments: []string{"raise exception", "permanent"},
	},
	{
		table:                lifecycleEffectClaimAuditSlotSlotsTable,
		name:                 "trg_recovery_point_lifecycle_audit_slots_transition",
		invalidReason:        "invalid_lifecycle_audit_slot_transition_trigger",
		triggerFragments:     []string{"before insert", lifecycleEffectClaimAuditSlotSlotsTable},
		sqliteWhen:           lifecycleAuditSlotTransitionSQLiteWhen,
		sqliteBody:           lifecycleAuditSlotTransitionSQLiteBody,
		postgresFunctionBody: lifecycleAuditSlotTransitionPostgresBody,
		postgresFunctionName: "recovery_point_lifecycle_audit_slot_transition_guard",
		sqliteFragments: []string{
			"when exists (",
			"from recovery_point_lifecycle_audit_slots",
			"status in ('deleted', 'already_absent')",
			"select raise(abort",
		},
		postgresFunctionFragments: []string{
			"perform 1",
			"from recovery_point_lifecycle_attempts",
			"where id = new.attempt_id",
			"for update",
			"if not found then",
			"if exists (",
			"from recovery_point_lifecycle_audit_slots",
			"status in ('deleted', 'already_absent')",
			"raise exception",
			"return new",
		},
	},
	{
		table:                     lifecycleEffectClaimAuditSlotSlotsTable,
		name:                      "trg_recovery_point_lifecycle_audit_slots_immutable_update",
		invalidReason:             "invalid_lifecycle_audit_slot_immutable_trigger",
		triggerFragments:          []string{"before update", lifecycleEffectClaimAuditSlotSlotsTable},
		sqliteBody:                lifecycleAuditSlotImmutableSQLiteBody,
		postgresFunctionBody:      lifecycleAuditSlotImmutablePostgresBody,
		postgresFunctionName:      "recovery_point_lifecycle_audit_slot_immutable_guard",
		sqliteFragments:           []string{"select raise(abort", "immutable"},
		postgresFunctionFragments: []string{"raise exception", "immutable"},
	},
	{
		table:                     lifecycleEffectClaimAuditSlotSlotsTable,
		name:                      "trg_recovery_point_lifecycle_audit_slots_immutable_delete",
		invalidReason:             "invalid_lifecycle_audit_slot_immutable_trigger",
		triggerFragments:          []string{"before delete", lifecycleEffectClaimAuditSlotSlotsTable},
		sqliteBody:                lifecycleAuditSlotPermanentSQLiteBody,
		postgresFunctionBody:      lifecycleAuditSlotImmutablePostgresBody,
		postgresFunctionName:      "recovery_point_lifecycle_audit_slot_immutable_guard",
		sqliteFragments:           []string{"select raise(abort", "permanent"},
		postgresFunctionFragments: []string{"raise exception", "immutable"},
	},
	{
		table:                "schema_migrations",
		name:                 lifecycleEffectClaimAuditSlotAdmissionTrigger,
		invalidReason:        "invalid_lifecycle_effect_claim_audit_slot_admission_trigger",
		triggerFragments:     []string{"before insert", "schema_migrations"},
		sqliteWhen:           lifecycleAdmissionSQLiteWhen,
		sqliteBody:           lifecycleAdmissionSQLiteBody,
		postgresFunctionBody: lifecycleAdmissionPostgresBody,
		postgresFunctionName: lifecycleEffectClaimAuditSlotAdmissionFunction,
		sqliteFragments: []string{
			"when new.version < 77",
			"exists (select 1 from " + lifecycleEffectClaimAuditSlotClaimsTable + ")",
			"exists (select 1 from " + lifecycleEffectClaimAuditSlotSlotsTable + ")",
			"select raise(abort",
		},
		postgresFunctionFragments: []string{
			"if new.version < 77 and (",
			"exists (select 1 from " + lifecycleEffectClaimAuditSlotClaimsTable + ")",
			"exists (select 1 from " + lifecycleEffectClaimAuditSlotSlotsTable + ")",
			"raise exception",
			"return new",
		},
	},
}

const plainTextContentAdmissionTrigger = "trg_backup_asset_plain_text_content_downgrade_admission"

const drillDurableRecoveryAdmissionTrigger = "trg_drill_durable_recovery_downgrade_admission"

var drillDurableRecoverySQLiteAdmissionFragments = []string{
	"before insert on schema_migrations",
	"when new.version < 74",
	"select 1 from task_runs",
	"where trigger_type = 'drill'",
	"and status in ('pending', 'running', 'retrying')",
	"raise(abort, '000074 downgrade blocked: active restore drill exists')",
}

var drillDurableRecoveryPostgresAdmissionTriggerFragments = []string{
	"before insert on",
	"schema_migrations",
	"execute function",
	"drill_durable_recovery_downgrade_admission()",
}

var drillDurableRecoveryPostgresAdmissionFunctionFragments = []string{
	"if new.version < 74 and exists",
	"select 1 from task_runs",
	"where trigger_type = 'drill'",
	"and status in ('pending', 'running', 'retrying')",
	"raise exception '000074 downgrade blocked: active restore drill exists'",
}

var plainTextContentSQLiteAdmissionFragments = []string{
	"before insert on schema_migrations",
	"when new.version < 73",
	"select 1 from backup_asset_delivery_grants",
	"where renderer = 'plain_text' or profile = 'text_v2'",
	"raise(abort, '000073 downgrade blocked: plain_text/text_v2 delivery grant exists')",
}

var plainTextContentPostgresAdmissionTriggerFragments = []string{
	"before insert on",
	"schema_migrations",
	"execute function",
	"backup_asset_plain_text_content_downgrade_admission()",
}

var plainTextContentPostgresAdmissionFunctionFragments = []string{
	"if new.version < 73",
	"select 1 from backup_asset_delivery_grants",
	"where renderer = 'plain_text' or profile = 'text_v2'",
	"raise exception '000073 downgrade blocked: plain_text/text_v2 delivery grant exists'",
}

var plainTextContentSQLiteCheckFragments = []string{
	"renderer in ('escaped_text', 'plain_text'",
	"profile in ('text_v1', 'text_v2'",
	"renderer = 'plain_text' and profile = 'text_v2' and range_policy = 'none'",
	"renderer in ('escaped_text', 'plain_text', 'metadata_hex')",
}

var plainTextContentPostgresConstraintFragments = map[string][]string{
	"backup_asset_delivery_grants_renderer_check":               {"plain_text"},
	"backup_asset_delivery_grants_profile_check":                {"text_v2"},
	"backup_asset_delivery_grants_renderer_product_check":       {"renderer", "plain_text", "profile", "text_v2", "range_policy", "none"},
	"backup_asset_delivery_grants_representation_product_check": {"renderer", "plain_text", "representation_source_bytes", "source_size"},
}

// ErrMigrationSchemaDrift means schema_migrations records a clean migration-69
// or newer database, but the minimum recovery schema is incomplete. The error is
// intentionally sanitized: callers can classify it with errors.Is without
// exposing SQL, DSNs, schema names, or business data.
var ErrMigrationSchemaDrift = errors.New("migration schema drift detected; startup refused")

var minimumRecoveryTables = []string{
	"backup_asset_recovery_plans",
	"backup_asset_recovery_plan_items",
	"backup_asset_recovery_preflights",
	"backup_asset_recovery_grants",
	"backup_asset_recovery_jobs",
	"backup_asset_recovery_job_items",
	"backup_asset_recovery_attempts",
	"backup_asset_recovery_checkpoints",
	"backup_asset_recovery_evidence",
	"backup_asset_recovery_result_sets",
	"backup_asset_recovery_results",
	"backup_asset_recovery_node_leases",
}

var minimumRecoveryTriggers = []struct {
	table string
	name  string
}{
	{table: "task_runs", name: "trg_backup_asset_recovery_task_runs_node_snapshot_insert"},
	{table: "task_runs", name: "trg_backup_asset_recovery_task_runs_node_snapshot_immutable"},
	{table: "schema_migrations", name: "trg_backup_asset_recovery_downgrade_admission"},
}

var taskRunCompatibilityTriggers = []struct {
	table string
	name  string
}{
	{table: "task_runs", name: "trg_backup_asset_task_runs_legacy_unknown_status_immutable"},
	{table: "schema_migrations", name: "trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission"},
}

// validateMinimumRecoverySchema is read-only. It runs before any historical
// fixup and again after Up so a falsely-clean version can never authorize
// forward writes or a successful startup.
func validateMinimumRecoverySchema(db *sql.DB, dbType string, version int64) error {
	if version < minimumRecoverySchemaVersion {
		return nil
	}

	columnExists, err := migrationColumnExists(db, dbType, "task_runs", "node_id_snapshot")
	if err != nil {
		return migrationSchemaDriftError(version, "catalog_query_failed")
	}
	if !columnExists {
		return migrationSchemaDriftError(version, "missing_task_run_snapshot_column")
	}

	for _, table := range minimumRecoveryTables {
		exists, existsErr := migrationRelationExists(db, dbType, table, "table")
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_recovery_table")
		}
	}

	snapshotIndex, indexErr := migrationIndexContractOf(db, dbType, "task_runs", "idx_task_runs_node_snapshot_status")
	if indexErr != nil {
		if errors.Is(indexErr, sql.ErrNoRows) {
			return migrationSchemaDriftError(version, "missing_task_run_snapshot_index")
		}
		return migrationSchemaDriftError(version, "catalog_query_failed")
	}
	if !migrationIndexUsable(snapshotIndex) ||
		snapshotIndex.unique ||
		!sameMigrationIndexColumns(snapshotIndex.columns, []string{"node_id_snapshot", "status"}) ||
		strings.TrimSpace(snapshotIndex.predicate) != "" {
		return migrationSchemaDriftError(version, "invalid_task_run_snapshot_index")
	}

	for _, trigger := range minimumRecoveryTriggers {
		exists, existsErr := migrationTriggerExists(db, dbType, trigger.table, trigger.name)
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_recovery_trigger")
		}
		if dbType == "postgres" {
			enabled, enabledErr := migrationTriggerEnabled(db, dbType, trigger.table, trigger.name)
			if enabledErr != nil {
				return migrationSchemaDriftError(version, "catalog_query_failed")
			}
			if !enabled {
				return migrationSchemaDriftError(version, "disabled_recovery_trigger")
			}
		}
	}
	if version < taskRunCompatibilitySchemaVersion {
		return nil
	}
	for _, trigger := range taskRunCompatibilityTriggers {
		exists, existsErr := migrationTriggerExists(db, dbType, trigger.table, trigger.name)
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_task_run_compatibility_trigger")
		}
		if dbType == "postgres" {
			enabled, enabledErr := migrationTriggerEnabled(db, dbType, trigger.table, trigger.name)
			if enabledErr != nil {
				return migrationSchemaDriftError(version, "catalog_query_failed")
			}
			if !enabled {
				return migrationSchemaDriftError(version, "disabled_task_run_compatibility_trigger")
			}
		}
	}
	if dbType == "postgres" {
		exists, existsErr := migrationConstraintExists(db, "task_runs", "task_runs_node_id_snapshot_compatibility")
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_task_run_compatibility_constraint")
		}
	}
	if version < plainTextContentSchemaVersion {
		return nil
	}
	if err := validatePlainTextContentChecks(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}
	if err := validatePlainTextContentAdmission(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}
	if version < drillDurableRecoverySchemaVersion {
		return nil
	}
	if err := validateDrillDurableRecoveryColumns(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}
	if err := validateDrillDurableRecoveryIndexes(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}
	if err := validateDrillDurableRecoveryAdmission(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}
	if version < lifecycleEffectClaimAuditSlotSchemaVersion {
		return nil
	}
	if err := validateLifecycleEffectClaimAuditSlotSchema(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}

	return nil
}

func validateLifecycleEffectClaimAuditSlotSchema(db *sql.DB, dbType string) error {
	for _, table := range []string{lifecycleEffectClaimAuditSlotClaimsTable, lifecycleEffectClaimAuditSlotSlotsTable} {
		exists, err := migrationRelationExists(db, dbType, table, "table")
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !exists {
			return errors.New("missing_lifecycle_effect_claim_audit_slot_table")
		}
	}

	claimColumns := []struct {
		name         string
		sqliteType   string
		postgresType string
		maxLength    int64
	}{
		{name: "id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "attempt_id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "executor_id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "execution_id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "transition_revision", sqliteType: "integer", postgresType: "bigint"},
		{name: "lease_id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "lease_attempt_id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "lease_fence_token_hash", sqliteType: "text", postgresType: "character varying", maxLength: 64},
		{name: "target_identity_digest", sqliteType: "text", postgresType: "character varying", maxLength: 64},
		{name: "state", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "deadline_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
		{name: "heartbeat_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
		{name: "created_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
		{name: "updated_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
	}
	if count, err := migrationColumnCount(db, dbType, lifecycleEffectClaimAuditSlotClaimsTable); err != nil {
		return errors.New("catalog_query_failed")
	} else if count != len(claimColumns) {
		return errors.New("invalid_lifecycle_effect_claim_columns")
	}
	for _, column := range claimColumns {
		if err := validateLifecycleColumn(db, dbType, lifecycleEffectClaimAuditSlotClaimsTable, column.name, column.sqliteType, column.postgresType, column.maxLength); err != nil {
			return err
		}
	}

	slotColumns := []struct {
		name         string
		sqliteType   string
		postgresType string
		maxLength    int64
	}{
		{name: "id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "attempt_id", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "status", sqliteType: "text", postgresType: "character varying", maxLength: 32},
		{name: "emitted_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
		{name: "created_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
	}
	if count, err := migrationColumnCount(db, dbType, lifecycleEffectClaimAuditSlotSlotsTable); err != nil {
		return errors.New("catalog_query_failed")
	} else if count != len(slotColumns) {
		return errors.New("invalid_lifecycle_audit_slot_columns")
	}
	for _, column := range slotColumns {
		if err := validateLifecycleColumn(db, dbType, lifecycleEffectClaimAuditSlotSlotsTable, column.name, column.sqliteType, column.postgresType, column.maxLength); err != nil {
			return err
		}
	}
	for _, primaryKey := range []struct {
		table  string
		reason string
	}{
		{table: lifecycleEffectClaimAuditSlotClaimsTable, reason: "invalid_lifecycle_effect_claim_primary_key"},
		{table: lifecycleEffectClaimAuditSlotSlotsTable, reason: "invalid_lifecycle_audit_slot_primary_key"},
	} {
		columns, err := migrationPrimaryKeyColumns(db, dbType, primaryKey.table)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !sameMigrationIndexColumns(columns, []string{"id"}) {
			return errors.New(primaryKey.reason)
		}
	}

	for _, forbidden := range []struct {
		table  string
		column string
	}{
		{table: lifecycleEffectClaimAuditSlotClaimsTable, column: "recovery_point_id"},
		{table: lifecycleEffectClaimAuditSlotClaimsTable, column: "operation"},
		{table: lifecycleEffectClaimAuditSlotClaimsTable, column: "phase"},
		{table: lifecycleEffectClaimAuditSlotClaimsTable, column: "audit_event_id"},
		{table: lifecycleEffectClaimAuditSlotSlotsTable, column: "audit_event_id"},
	} {
		exists, err := migrationColumnExists(db, dbType, forbidden.table, forbidden.column)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if exists {
			return errors.New("unexpected_lifecycle_effect_claim_audit_slot_column")
		}
	}

	if count, err := migrationForeignKeyCount(db, dbType, lifecycleEffectClaimAuditSlotClaimsTable); err != nil {
		return errors.New("catalog_query_failed")
	} else if count != 1 {
		return errors.New("invalid_lifecycle_effect_claim_foreign_keys")
	}
	if count, err := migrationForeignKeyCount(db, dbType, lifecycleEffectClaimAuditSlotSlotsTable); err != nil {
		return errors.New("catalog_query_failed")
	} else if count != 1 {
		return errors.New("invalid_lifecycle_audit_slot_foreign_keys")
	}
	for _, table := range []string{lifecycleEffectClaimAuditSlotClaimsTable, lifecycleEffectClaimAuditSlotSlotsTable} {
		if ok, err := migrationForeignKeyExists(db, dbType, table, "attempt_id", "recovery_point_lifecycle_attempts"); err != nil {
			return errors.New("catalog_query_failed")
		} else if !ok {
			return errors.New("missing_lifecycle_effect_claim_audit_slot_attempt_foreign_key")
		}
	}

	claimAttemptIndex, err := migrationIndexContractOf(db, dbType, lifecycleEffectClaimAuditSlotClaimsTable, "idx_recovery_point_lifecycle_effect_claims_attempt")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_lifecycle_effect_claim_attempt_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(claimAttemptIndex) ||
		!claimAttemptIndex.unique ||
		!sameMigrationIndexColumns(claimAttemptIndex.columns, []string{"attempt_id"}) ||
		strings.TrimSpace(claimAttemptIndex.predicate) != "" {
		return errors.New("invalid_lifecycle_effect_claim_attempt_index")
	}
	claimStateIndex, err := migrationIndexContractOf(db, dbType, lifecycleEffectClaimAuditSlotClaimsTable, "idx_recovery_point_lifecycle_effect_claims_state_deadline")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_lifecycle_effect_claim_state_deadline_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(claimStateIndex) ||
		claimStateIndex.unique ||
		!sameMigrationIndexColumns(claimStateIndex.columns, []string{"state", "deadline_at"}) {
		return errors.New("invalid_lifecycle_effect_claim_state_deadline_index")
	}
	slotAttemptStatusIndex, err := migrationIndexContractOf(db, dbType, lifecycleEffectClaimAuditSlotSlotsTable, "idx_recovery_point_lifecycle_audit_slots_attempt_status")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_lifecycle_audit_slot_attempt_status_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(slotAttemptStatusIndex) ||
		!slotAttemptStatusIndex.unique ||
		!sameMigrationIndexColumns(slotAttemptStatusIndex.columns, []string{"attempt_id", "status"}) ||
		strings.TrimSpace(slotAttemptStatusIndex.predicate) != "" {
		return errors.New("invalid_lifecycle_audit_slot_attempt_status_index")
	}
	slotTerminalIndex, err := migrationIndexContractOf(db, dbType, lifecycleEffectClaimAuditSlotSlotsTable, "idx_recovery_point_lifecycle_audit_slots_terminal")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_lifecycle_audit_slot_terminal_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(slotTerminalIndex) ||
		!slotTerminalIndex.unique ||
		!sameMigrationIndexColumns(slotTerminalIndex.columns, []string{"attempt_id"}) ||
		!lifecycleAuditSlotTerminalPredicateExact(dbType, slotTerminalIndex.predicate) {
		return errors.New("invalid_lifecycle_audit_slot_terminal_index")
	}
	claimChecks, err := migrationTableCheckDefinitions(db, dbType, lifecycleEffectClaimAuditSlotClaimsTable)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if err := validateLifecycleEffectClaimCheckDefinitions(dbType, claimChecks, []string{
		"id", "attempt_id", "executor_id", "execution_id", "lease_id", "lease_attempt_id",
	}, []string{"lease_fence_token_hash", "target_identity_digest"}); err != nil {
		return err
	}

	slotChecks, err := migrationTableCheckDefinitions(db, dbType, lifecycleEffectClaimAuditSlotSlotsTable)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if err := validateLifecycleAuditSlotCheckDefinitions(dbType, slotChecks); err != nil {
		return err
	}

	for _, trigger := range lifecycleEffectClaimAuditSlotTriggerContracts {
		definition, err := migrationTriggerDefinition(db, dbType, trigger.table, trigger.name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("missing_lifecycle_effect_claim_audit_slot_trigger")
			}
			return errors.New("catalog_query_failed")
		}
		if dbType == "postgres" {
			enabled, enabledErr := migrationTriggerEnabled(db, dbType, trigger.table, trigger.name)
			if enabledErr != nil {
				return errors.New("catalog_query_failed")
			}
			if !enabled {
				return errors.New(trigger.invalidReason)
			}
		}
		normalizedTrigger := normalizeMigrationDefinition(definition)
		if !containsMigrationFragments(normalizedTrigger, trigger.triggerFragments) {
			return errors.New(trigger.invalidReason)
		}
		if dbType == "sqlite" {
			if !containsMigrationFragments(normalizedTrigger, trigger.sqliteFragments) ||
				!containsMigrationFragmentCounts(normalizedTrigger, trigger.sqliteFragmentMinimumCounts) ||
				!lifecycleSQLiteGuardDefinitionExact(definition, trigger) {
				return errors.New(trigger.invalidReason)
			}
			continue
		}
		if !lifecyclePostgresGuardDefinitionExact(definition, trigger) ||
			!containsMigrationFragments(normalizedTrigger, []string{"for each row", "execute function", migrationCatalogIdentifier("postgres", trigger.postgresFunctionName) + "()"}) {
			return errors.New(trigger.invalidReason)
		}
		functionDefinition, functionErr := migrationTriggerFunctionDefinition(db, trigger.table, trigger.name)
		if functionErr != nil {
			return errors.New("catalog_query_failed")
		}
		normalizedFunction := normalizeMigrationDefinition(functionDefinition)
		if !containsMigrationFragments(normalizedFunction, []string{"returns trigger", "language plpgsql"}) ||
			!containsMigrationFragments(normalizedFunction, trigger.postgresFunctionFragments) ||
			!containsMigrationFragmentCounts(normalizedFunction, trigger.postgresFunctionFragmentMinimumCounts) ||
			!lifecyclePostgresGuardFunctionDefinitionExact(functionDefinition, trigger) {
			return errors.New(trigger.invalidReason)
		}
	}
	for _, trigger := range []struct {
		table string
		name  string
	}{
		{table: "schema_migrations", name: "trg_backup_asset_lifecycle_downgrade_admission"},
		{table: "schema_migrations", name: "trg_backup_asset_ga_downgrade_admission"},
		{table: "schema_migrations", name: "trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission"},
		{table: "schema_migrations", name: plainTextContentAdmissionTrigger},
		{table: "schema_migrations", name: drillDurableRecoveryAdmissionTrigger},
		{table: "schema_migrations", name: "trg_rclone_native_version_evidence_downgrade_admission"},
		{table: "schema_migrations", name: "trg_provider_native_version_reference_reason_downgrade_admission"},
	} {
		definition, err := migrationTriggerDefinition(db, dbType, trigger.table, trigger.name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("missing_lifecycle_effect_claim_audit_slot_trigger")
			}
			return errors.New("catalog_query_failed")
		}
		if dbType == "postgres" {
			enabled, enabledErr := migrationTriggerEnabled(db, dbType, trigger.table, trigger.name)
			if enabledErr != nil {
				return errors.New("catalog_query_failed")
			}
			if !enabled {
				return errors.New("invalid_lifecycle_effect_claim_audit_slot_trigger")
			}
		}
		normalized := normalizeMigrationDefinition(definition)
		if !containsMigrationFragments(normalized, []string{"before insert", "schema_migrations"}) {
			return errors.New("invalid_lifecycle_effect_claim_audit_slot_trigger")
		}
		if dbType == "postgres" {
			functionDefinition, functionErr := migrationTriggerFunctionDefinition(db, trigger.table, trigger.name)
			if functionErr != nil {
				return errors.New("catalog_query_failed")
			}
			normalizedFunction := normalizeMigrationDefinition(functionDefinition)
			if trigger.name == "trg_provider_native_version_reference_reason_downgrade_admission" {
				if !containsMigrationFragments(normalizedFunction, []string{"new.version < 76", "provider_native_version_referenced", "return new"}) {
					return errors.New("invalid_provider_native_version_reference_reason_admission_trigger")
				}
			} else if !strings.Contains(normalizedFunction, "return new") {
				return errors.New("invalid_lifecycle_effect_claim_audit_slot_trigger")
			}
		}
	}
	return nil
}

func containsMigrationFragments(normalized string, fragments []string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(normalized, normalizeMigrationDefinition(fragment)) {
			return false
		}
	}
	return true
}

func containsMigrationFragmentCounts(normalized string, minimumCounts map[string]int) bool {
	for fragment, minimum := range minimumCounts {
		if strings.Count(normalized, normalizeMigrationDefinition(fragment)) < minimum {
			return false
		}
	}
	return true
}

func lifecycleSQLiteGuardDefinitionExact(definition string, contract lifecycleEffectClaimAuditSlotTriggerContract) bool {
	begin := migrationSQLKeywordIndex(definition, "begin", 0)
	if begin < 0 {
		return false
	}
	end := migrationSQLKeywordLastIndex(definition, "end", begin+len("begin"))
	if end < 0 {
		return false
	}

	prefix := definition[:begin]
	when := migrationSQLKeywordIndex(prefix, "when", 0)
	header := prefix
	actualWhen := ""
	if when >= 0 {
		header = prefix[:when]
		actualWhen = prefix[when+len("when"):]
	}
	expectedHeader := fmt.Sprintf(
		"CREATE TRIGGER %s %s ON %s",
		contract.name,
		contract.triggerFragments[0],
		contract.table,
	)
	if normalizeMigrationGuardText(header) != normalizeMigrationGuardText(expectedHeader) ||
		normalizeMigrationGuardText(actualWhen) != normalizeMigrationGuardText(contract.sqliteWhen) {
		return false
	}

	body := definition[begin+len("begin") : end]
	if normalizeMigrationGuardText(body) != normalizeMigrationGuardText(contract.sqliteBody) {
		return false
	}
	return strings.Trim(strings.TrimSpace(definition[end+len("end"):]), "; \t\r\n") == ""
}
func lifecyclePostgresGuardDefinitionExact(
	definition string,
	contract lifecycleEffectClaimAuditSlotTriggerContract,
) bool {
	normalized := strings.TrimSuffix(strings.TrimSpace(normalizeMigrationGuardText(definition)), ";")
	tokens := strings.Fields(normalized)
	if len(tokens) != 13 {
		return false
	}
	event := strings.Fields(normalizeMigrationGuardText(contract.triggerFragments[0]))
	if len(event) != 2 {
		return false
	}
	function := strings.TrimSuffix(tokens[12], "()")
	return tokens[0] == "create" &&
		tokens[1] == "trigger" &&
		tokens[2] == migrationCatalogIdentifier("postgres", contract.name) &&
		tokens[3] == event[0] &&
		tokens[4] == event[1] &&
		tokens[5] == "on" &&
		migrationUnqualifiedCatalogIdentifier(tokens[6]) == contract.table &&
		tokens[7] == "for" &&
		tokens[8] == "each" &&
		tokens[9] == "row" &&
		tokens[10] == "execute" &&
		tokens[11] == "function" &&
		migrationUnqualifiedCatalogIdentifier(function) ==
			migrationCatalogIdentifier("postgres", contract.postgresFunctionName)
}

func migrationUnqualifiedCatalogIdentifier(identifier string) string {
	identifier = strings.Trim(identifier, `"`)
	if dot := strings.LastIndex(identifier, "."); dot >= 0 {
		identifier = identifier[dot+1:]
	}
	return strings.Trim(identifier, `"`)
}

func normalizeMigrationGuardBody(body string) string {
	return strings.TrimSuffix(strings.TrimSpace(normalizeMigrationGuardText(body)), ";")
}

func lifecyclePostgresGuardFunctionDefinitionExact(
	definition string,
	contract lifecycleEffectClaimAuditSlotTriggerContract,
) bool {
	body, ok := migrationPostgresFunctionBody(definition)
	if !ok {
		return false
	}
	return normalizeMigrationGuardBody(body) == normalizeMigrationGuardBody(contract.postgresFunctionBody)
}

func migrationPostgresFunctionBody(definition string) (string, bool) {
	as := migrationSQLKeywordIndex(definition, "as", 0)
	if as < 0 {
		return "", false
	}
	tagStartRel := strings.Index(definition[as+len("as"):], "$")
	if tagStartRel < 0 {
		return "", false
	}
	tagStart := as + len("as") + tagStartRel
	tagEndRel := strings.Index(definition[tagStart+1:], "$")
	if tagEndRel < 0 {
		return "", false
	}
	tag := definition[tagStart : tagStart+1+tagEndRel+1]
	bodyStart := tagStart + len(tag)
	bodyEnd := strings.LastIndex(definition, tag)
	if bodyEnd <= bodyStart {
		return "", false
	}
	if strings.Trim(strings.TrimSpace(definition[bodyEnd+len(tag):]), "; \t\r\n") != "" {
		return "", false
	}
	return definition[bodyStart:bodyEnd], true
}

func migrationSQLKeywordIndex(definition, keyword string, start int) int {
	if start < 0 {
		start = 0
	}
	definition = strings.ToLower(definition)
	keyword = strings.ToLower(keyword)
	for start < len(definition) {
		offset := strings.Index(definition[start:], keyword)
		if offset < 0 {
			return -1
		}
		index := start + offset
		end := index + len(keyword)
		if (index == 0 || !migrationSQLWordCharacter(definition[index-1])) &&
			(end == len(definition) || !migrationSQLWordCharacter(definition[end])) {
			return index
		}
		start = end
	}
	return -1
}

func migrationSQLKeywordLastIndex(definition, keyword string, start int) int {
	last := -1
	for index := migrationSQLKeywordIndex(definition, keyword, start); index >= 0; {
		last = index
		index = migrationSQLKeywordIndex(definition, keyword, index+len(keyword))
	}
	return last
}

func migrationSQLWordCharacter(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= '0' && character <= '9') ||
		character == '_'
}

func normalizeMigrationGuardText(definition string) string {
	return normalizeMigrationDefinition(stripMigrationSQLComments(definition))
}

func stripMigrationSQLComments(definition string) string {
	var builder strings.Builder
	builder.Grow(len(definition))
	inSingleQuote := false
	inDoubleQuote := false
	for index := 0; index < len(definition); {
		character := definition[index]
		if inSingleQuote {
			builder.WriteByte(character)
			index++
			if character == '\'' {
				if index < len(definition) && definition[index] == '\'' {
					builder.WriteByte(definition[index])
					index++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			builder.WriteByte(character)
			index++
			if character == '"' {
				if index < len(definition) && definition[index] == '"' {
					builder.WriteByte(definition[index])
					index++
				} else {
					inDoubleQuote = false
				}
			}
			continue
		}
		if character == '\'' {
			inSingleQuote = true
			builder.WriteByte(character)
			index++
			continue
		}
		if character == '"' {
			inDoubleQuote = true
			builder.WriteByte(character)
			index++
			continue
		}
		if character == '-' && index+1 < len(definition) && definition[index+1] == '-' {
			index += 2
			for index < len(definition) && definition[index] != '\n' {
				index++
			}
			continue
		}
		if character == '/' && index+1 < len(definition) && definition[index+1] == '*' {
			index += 2
			for index+1 < len(definition) &&
				(definition[index] != '*' || definition[index+1] != '/') {
				index++
			}
			if index+1 < len(definition) {
				index += 2
			}
			continue
		}
		builder.WriteByte(character)

		index++
	}
	return builder.String()
}

func validateLifecycleEffectClaimCheckDefinitions(
	dbType string,
	checks []migrationCheckDefinition,
	id32Columns,
	digest64Columns []string,
) error {
	expected := make([]string, 0, len(id32Columns)+len(digest64Columns)+2)
	for _, column := range id32Columns {
		expected = append(expected, lifecycleCanonicalHexCheckExpression(dbType, column, 32))
	}
	for _, column := range digest64Columns {
		expected = append(expected, lifecycleCanonicalHexCheckExpression(dbType, column, 64))
	}
	switch dbType {
	case "sqlite":
		expected = append(expected,
			normalizeMigrationPredicate("transition_revision > 0"),
			normalizeMigrationPredicate("state IN ('in_flight', 'uncertain', 'proven')"),
		)
	case "postgres":
		expected = append(expected,
			normalizeMigrationPredicate("transition_revision > 0"),
			normalizeMigrationPredicate("state = ANY (ARRAY['in_flight', 'uncertain', 'proven'])"),
		)
	default:
		return errors.New("catalog_query_failed")
	}
	if !migrationCheckMultisetExact(dbType, checks, expected) {
		return errors.New("invalid_lifecycle_effect_claim_check")
	}
	return nil
}

func validateLifecycleAuditSlotCheckDefinitions(
	dbType string,
	checks []migrationCheckDefinition,
) error {
	var expected []string
	switch dbType {
	case "sqlite":
		expected = []string{
			lifecycleCanonicalHexCheckExpression(dbType, "id", 32),
			lifecycleCanonicalHexCheckExpression(dbType, "attempt_id", 32),
			normalizeMigrationPredicate("status IN ('deleted', 'already_absent', 'blocked', 'identity_conflict')"),
		}
	case "postgres":
		expected = []string{
			lifecycleCanonicalHexCheckExpression(dbType, "id", 32),
			lifecycleCanonicalHexCheckExpression(dbType, "attempt_id", 32),
			normalizeMigrationPredicate("status = ANY (ARRAY['deleted', 'already_absent', 'blocked', 'identity_conflict'])"),
		}
	default:
		return errors.New("catalog_query_failed")
	}
	if !migrationCheckMultisetExact(dbType, checks, expected) {
		return errors.New("invalid_lifecycle_audit_slot_check")
	}
	return nil
}

func lifecycleCanonicalHexCheckExpression(dbType, column string, length int64) string {
	if dbType == "sqlite" {
		return normalizeMigrationPredicate(
			fmt.Sprintf("length(%s) = %d AND %s NOT GLOB '*[^0-9a-f]*'", column, length, column),
		)
	}
	return normalizeMigrationPredicate(fmt.Sprintf("%s ~ '^[0-9a-f]{%d}$'", column, length))
}

func migrationCheckMultisetExact(
	dbType string,
	checks []migrationCheckDefinition,
	expected []string,
) bool {
	if len(checks) != len(expected) {
		return false
	}
	gotCounts := make(map[string]int, len(checks))
	for _, check := range checks {
		if dbType == "postgres" && !check.validated {
			return false
		}
		gotCounts[normalizeMigrationCheckExpression(dbType, check.expression)]++
	}
	wantCounts := make(map[string]int, len(expected))
	for _, expression := range expected {
		wantCounts[normalizeMigrationCheckExpression(dbType, expression)]++
	}
	if len(gotCounts) != len(wantCounts) {
		return false
	}
	for expression, count := range wantCounts {
		if gotCounts[expression] != count {
			return false
		}
	}
	return true
}

func normalizeMigrationCheckExpression(dbType, expression string) string {
	normalized := normalizeMigrationPredicate(expression)
	if dbType == "postgres" {
		normalized = strings.ReplaceAll(normalized, "::bigint", "")
		normalized = strings.ReplaceAll(normalized, "::integer", "")
	}
	return normalized
}

func validateLifecycleColumn(db *sql.DB, dbType, table, column, sqliteType, postgresType string, maxLength int64) error {
	contract, err := migrationColumnContractOf(db, dbType, table, column)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_lifecycle_effect_claim_audit_slot_column")
		}
		return errors.New("catalog_query_failed")
	}
	wantType := sqliteType
	if dbType == "postgres" {
		wantType = postgresType
		if maxLength > 0 && contract.maxLength != maxLength {
			return errors.New("invalid_lifecycle_effect_claim_audit_slot_column")
		}
	}
	if contract.dataType != wantType || !contract.notNull || normalizeMigrationSQLToken(contract.defaultSQL) != "" {
		return errors.New("invalid_lifecycle_effect_claim_audit_slot_column")
	}
	return nil
}

func sameMigrationIndexColumns(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type migrationColumnContract struct {
	dataType   string
	maxLength  int64
	notNull    bool
	defaultSQL string
}

func validateDrillDurableRecoveryColumns(db *sql.DB, dbType string) error {
	owner, err := migrationColumnContractOf(db, dbType, "restore_drill_evidences", "recovery_owner_id")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_drill_recovery_owner_column")
		}
		return errors.New("catalog_query_failed")
	}
	ownerDefault := normalizeMigrationSQLToken(owner.defaultSQL)
	ownerTypeOK := owner.dataType == "text" && dbType == "sqlite"
	if dbType == "postgres" {
		ownerTypeOK = owner.dataType == "character varying" && owner.maxLength == 64
	}
	if !ownerTypeOK || !owner.notNull || (ownerDefault != "''" && ownerDefault != "''::charactervarying") {
		return errors.New("invalid_drill_recovery_owner_column")
	}

	lease, err := migrationColumnContractOf(db, dbType, "restore_drill_evidences", "recovery_lease_until")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_drill_recovery_lease_column")
		}
		return errors.New("catalog_query_failed")
	}
	leaseTypeOK := lease.dataType == "datetime" && dbType == "sqlite"
	if dbType == "postgres" {
		leaseTypeOK = lease.dataType == "timestamp with time zone"
	}
	if !leaseTypeOK || lease.notNull || normalizeMigrationSQLToken(lease.defaultSQL) != "" {
		return errors.New("invalid_drill_recovery_lease_column")
	}
	return nil
}

type migrationIndexContract struct {
	unique    bool
	valid     bool
	ready     bool
	live      bool
	columns   []string
	predicate string
}

func migrationIndexUsable(contract migrationIndexContract) bool {
	return contract.valid && contract.ready && contract.live
}

func validateDrillDurableRecoveryIndexes(db *sql.DB, dbType string) error {
	active, err := migrationIndexContractOf(db, dbType, "task_runs", "idx_task_runs_active_drill")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_active_drill_index")
		}
		return errors.New("catalog_query_failed")
	}
	wantPredicate := "trigger_type='drill'andstatusin'pending','running','retrying'"
	if dbType == "postgres" {
		wantPredicate = "trigger_type='drill'andstatus=anyarray['pending','running','retrying']"
	}
	if !migrationIndexUsable(active) ||
		!active.unique || strings.Join(active.columns, ",") != "task_id" || normalizeMigrationPredicate(active.predicate) != wantPredicate {
		return errors.New("invalid_active_drill_index")
	}

	lease, err := migrationIndexContractOf(db, dbType, "restore_drill_evidences", "idx_restore_drill_recovery_lease")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_drill_recovery_lease_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(lease) ||
		lease.unique || strings.Join(lease.columns, ",") != "recovery_lease_until" || strings.TrimSpace(lease.predicate) != "" {
		return errors.New("invalid_drill_recovery_lease_index")
	}
	return nil
}

func validateDrillDurableRecoveryAdmission(db *sql.DB, dbType string) error {
	definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", drillDurableRecoveryAdmissionTrigger)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_drill_recovery_admission_trigger")
		}
		return errors.New("catalog_query_failed")
	}
	if dbType == "postgres" {
		enabled, enabledErr := migrationTriggerEnabled(db, dbType, "schema_migrations", drillDurableRecoveryAdmissionTrigger)
		if enabledErr != nil {
			return errors.New("catalog_query_failed")
		}
		if !enabled {
			return errors.New("invalid_drill_recovery_admission_trigger")
		}
	}
	fragments := drillDurableRecoverySQLiteAdmissionFragments
	if dbType == "postgres" {
		fragments = drillDurableRecoveryPostgresAdmissionTriggerFragments
	}
	definition = normalizeMigrationDefinition(definition)
	for _, fragment := range fragments {
		if !strings.Contains(definition, fragment) {
			return errors.New("invalid_drill_recovery_admission_trigger")
		}
	}
	if dbType != "postgres" {
		return nil
	}

	functionDefinition, functionErr := migrationTriggerFunctionDefinition(
		db,
		"schema_migrations",
		drillDurableRecoveryAdmissionTrigger,
	)
	if functionErr != nil {
		if errors.Is(functionErr, sql.ErrNoRows) {
			return errors.New("invalid_drill_recovery_admission_trigger")
		}
		return errors.New("catalog_query_failed")
	}
	functionDefinition = normalizeMigrationDefinition(functionDefinition)
	for _, fragment := range drillDurableRecoveryPostgresAdmissionFunctionFragments {
		if !strings.Contains(functionDefinition, fragment) {
			return errors.New("invalid_drill_recovery_admission_trigger")
		}
	}
	return nil
}

func validatePlainTextContentAdmission(db *sql.DB, dbType string) error {
	definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", plainTextContentAdmissionTrigger)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_plain_text_content_admission_trigger")
		}
		return errors.New("catalog_query_failed")
	}
	if dbType == "postgres" {
		enabled, enabledErr := migrationTriggerEnabled(db, dbType, "schema_migrations", plainTextContentAdmissionTrigger)
		if enabledErr != nil {
			return errors.New("catalog_query_failed")
		}
		if !enabled {
			return errors.New("invalid_plain_text_content_admission_trigger")
		}
	}

	fragments := plainTextContentSQLiteAdmissionFragments
	if dbType == "postgres" {
		fragments = plainTextContentPostgresAdmissionTriggerFragments
	}
	definition = normalizeMigrationDefinition(definition)
	for _, fragment := range fragments {
		if !strings.Contains(definition, fragment) {
			return errors.New("invalid_plain_text_content_admission_trigger")
		}
	}

	if dbType == "postgres" {
		functionDefinition, functionErr := migrationTriggerFunctionDefinition(
			db,
			"schema_migrations",
			plainTextContentAdmissionTrigger,
		)
		if functionErr != nil {
			if errors.Is(functionErr, sql.ErrNoRows) {
				return errors.New("invalid_plain_text_content_admission_trigger")
			}
			return errors.New("catalog_query_failed")
		}
		functionDefinition = normalizeMigrationDefinition(functionDefinition)
		for _, fragment := range plainTextContentPostgresAdmissionFunctionFragments {
			if !strings.Contains(functionDefinition, fragment) {
				return errors.New("invalid_plain_text_content_admission_trigger")
			}
		}
	}
	return nil
}

func normalizeMigrationDefinition(definition string) string {
	return strings.Join(strings.Fields(strings.ToLower(definition)), " ")
}

func normalizeMigrationSQLToken(definition string) string {
	definition = strings.ToLower(definition)
	replacer := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "", "(", "", ")", "")
	return replacer.Replace(definition)
}

func normalizeMigrationPredicate(predicate string) string {
	predicate = strings.ToLower(predicate)
	predicate = strings.ReplaceAll(predicate, "::character varying[]", "")
	predicate = strings.ReplaceAll(predicate, "::text[]", "")
	predicate = strings.ReplaceAll(predicate, "::character varying", "")
	predicate = strings.ReplaceAll(predicate, "::text", "")
	replacer := strings.NewReplacer(
		" ", "",
		"\t", "",
		"\r", "",
		"\n", "",
		"(", "",
		")", "",
		`"`, "",
	)
	return replacer.Replace(predicate)
}

// lifecycleAuditSlotTerminalPredicateExact accepts only the two equivalent
// catalog renderings of the migration's partial unique predicate. A substring
// check is not sufficient: one-sided and broadened predicates would silently
// weaken the one-terminal-per-attempt invariant.
func lifecycleAuditSlotTerminalPredicateExact(dbType, predicate string) bool {
	normalized := normalizeMigrationPredicate(predicate)
	switch dbType {
	case "sqlite":
		return normalized == "statusin'deleted','already_absent'" ||
			normalized == "statusin'already_absent','deleted'"
	case "postgres":
		return normalized == "status=anyarray['deleted','already_absent']" ||
			normalized == "status=anyarray['already_absent','deleted']"
	default:
		return false
	}
}

func validatePlainTextContentChecks(db *sql.DB, dbType string) error {
	switch dbType {
	case "sqlite":
		var definition string
		if err := db.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'backup_asset_delivery_grants'",
		).Scan(&definition); err != nil {
			return errors.New("catalog_query_failed")
		}
		definition = strings.ToLower(definition)
		for _, fragment := range plainTextContentSQLiteCheckFragments {
			if !strings.Contains(definition, fragment) {
				return errors.New("missing_plain_text_content_constraint")
			}
		}
	case "postgres":
		for constraint, fragments := range plainTextContentPostgresConstraintFragments {
			definition, err := migrationConstraintDefinition(db, "backup_asset_delivery_grants", constraint)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("missing_plain_text_content_constraint")
				}
				return errors.New("catalog_query_failed")
			}
			definition = strings.ToLower(definition)
			for _, fragment := range fragments {
				if !strings.Contains(definition, fragment) {
					return errors.New("missing_plain_text_content_constraint")
				}
			}
		}
	default:
		return errors.New("catalog_query_failed")
	}
	return nil
}

func migrationSchemaDriftError(version int64, reason string) error {
	return fmt.Errorf("%w (version=%d, reason=%s); restore a verified backup or perform an audited offline repair", ErrMigrationSchemaDrift, version, reason)
}

type migrationCheckDefinition struct {
	expression string
	validated  bool
}

func migrationTableCheckDefinitions(db *sql.DB, dbType, table string) ([]migrationCheckDefinition, error) {
	switch dbType {
	case "sqlite":
		var definition string
		if err := db.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		).Scan(&definition); err != nil {
			return nil, err
		}
		expressions := migrationExtractCheckExpressions(definition)
		checks := make([]migrationCheckDefinition, 0, len(expressions))
		for _, expression := range expressions {
			checks = append(checks, migrationCheckDefinition{expression: expression, validated: true})
		}
		return checks, nil
	case "postgres":
		rows, err := db.Query(`
			SELECT pg_catalog.pg_get_constraintdef(constraint_row.oid),
			       constraint_row.convalidated
			FROM pg_catalog.pg_constraint AS constraint_row
			JOIN pg_catalog.pg_class AS relation ON relation.oid = constraint_row.conrelid
			JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND constraint_row.contype = 'c'
			ORDER BY constraint_row.conname, constraint_row.oid`, table)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var checks []migrationCheckDefinition
		for rows.Next() {
			var definition string
			var validated bool
			if err := rows.Scan(&definition, &validated); err != nil {
				return nil, err
			}
			expressions := migrationExtractCheckExpressions(definition)
			if len(expressions) != 1 {
				return nil, errors.New("invalid_check_definition")
			}
			checks = append(checks, migrationCheckDefinition{
				expression: expressions[0],
				validated:  validated,
			})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return checks, nil
	default:
		return nil, fmt.Errorf("unsupported database type")
	}
}

func migrationExtractCheckExpressions(definition string) []string {
	var expressions []string
	for index := 0; index < len(definition); {
		checkAt := migrationCheckKeywordIndex(definition, index)
		if checkAt < 0 {
			break
		}
		open := checkAt + len("check")
		for open < len(definition) {
			switch definition[open] {
			case ' ', '\t', '\r', '\n':
				open++
			default:
				goto checkOpen
			}
		}
	checkOpen:
		if open >= len(definition) || definition[open] != '(' {
			index = checkAt + len("check")
			continue
		}
		close, ok := migrationMatchingParenthesis(definition, open)
		if !ok {
			break
		}
		expressions = append(expressions, definition[open+1:close])
		index = close + 1
	}
	return expressions
}

func migrationCheckKeywordIndex(definition string, start int) int {
	inSingleQuote := false
	inDoubleQuote := false
	for index := start; index+len("check") <= len(definition); index++ {
		character := definition[index]
		if inSingleQuote {
			if character == '\'' {
				if index+1 < len(definition) && definition[index+1] == '\'' {
					index++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			if character == '"' {
				if index+1 < len(definition) && definition[index+1] == '"' {
					index++
				} else {
					inDoubleQuote = false
				}
			}
			continue
		}
		if character == '\'' {
			inSingleQuote = true
			continue
		}
		if character == '"' {
			inDoubleQuote = true
			continue
		}
		if !strings.EqualFold(definition[index:index+len("check")], "check") ||
			(index > 0 && migrationSQLWordCharacter(definition[index-1])) ||
			(index+len("check") < len(definition) && migrationSQLWordCharacter(definition[index+len("check")])) {
			continue
		}
		return index
	}
	return -1
}

func migrationMatchingParenthesis(definition string, open int) (int, bool) {
	depth := 0
	inSingleQuote := false
	inDoubleQuote := false
	for index := open; index < len(definition); index++ {
		character := definition[index]
		if inSingleQuote {
			if character == '\'' {
				if index+1 < len(definition) && definition[index+1] == '\'' {
					index++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			if character == '"' {
				if index+1 < len(definition) && definition[index+1] == '"' {
					index++
				} else {
					inDoubleQuote = false
				}
			}
			continue
		}
		switch character {
		case '\'':
			inSingleQuote = true
		case '"':
			inDoubleQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index, true
			}
		}
	}
	return -1, false
}

func migrationForeignKeyCount(db *sql.DB, dbType, table string) (int, error) {
	switch dbType {
	case "sqlite":
		rows, err := db.Query("PRAGMA foreign_key_list('" + table + "')")
		if err != nil {
			return 0, err
		}
		defer func() { _ = rows.Close() }()
		seen := make(map[int]struct{})
		for rows.Next() {
			var id, seq int
			var target, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				return 0, err
			}
			seen[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return len(seen), nil
	case "postgres":
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_constraint AS constraint_row
			JOIN pg_catalog.pg_class AS relation ON relation.oid = constraint_row.conrelid
			JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND constraint_row.contype = 'f'`, table).Scan(&count)
		return count, err
	default:
		return 0, fmt.Errorf("unsupported database type")
	}
}

func migrationForeignKeyExists(db *sql.DB, dbType, table, column, targetTable string) (bool, error) {
	switch dbType {
	case "sqlite":
		rows, err := db.Query("PRAGMA foreign_key_list('" + table + "')")
		if err != nil {
			return false, err
		}
		defer func() { _ = rows.Close() }()
		mappingCount := 0
		valid := true
		for rows.Next() {
			var id, seq int
			var target, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				return false, err
			}
			mappingCount++
			if seq != 0 ||
				from != column ||
				target != targetTable ||
				to != "id" ||
				!strings.EqualFold(onUpdate, "NO ACTION") ||
				!strings.EqualFold(onDelete, "RESTRICT") ||
				!strings.EqualFold(match, "NONE") {
				valid = false
			}
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		return mappingCount == 1 && valid, nil
	case "postgres":
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_constraint AS constraint_row
			JOIN pg_catalog.pg_class AS relation
			  ON relation.oid = constraint_row.conrelid
			JOIN pg_catalog.pg_namespace AS namespace
			  ON namespace.oid = relation.relnamespace
			JOIN pg_catalog.pg_class AS target_relation
			  ON target_relation.oid = constraint_row.confrelid
			JOIN pg_catalog.pg_namespace AS target_namespace
			  ON target_namespace.oid = target_relation.relnamespace
			JOIN LATERAL unnest(constraint_row.conkey) WITH ORDINALITY
			  AS local_key(attnum, ordinal) ON TRUE
			JOIN LATERAL unnest(constraint_row.confkey) WITH ORDINALITY
			  AS target_key(attnum, ordinal)
			  ON target_key.ordinal = local_key.ordinal
			JOIN pg_catalog.pg_attribute AS local_attribute
			  ON local_attribute.attrelid = relation.oid
			 AND local_attribute.attnum = local_key.attnum
			JOIN pg_catalog.pg_attribute AS target_attribute
			  ON target_attribute.attrelid = target_relation.oid
			 AND target_attribute.attnum = target_key.attnum
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND target_namespace.nspname = current_schema()
			  AND target_relation.relname = $3
			  AND constraint_row.contype = 'f'
			  AND array_length(constraint_row.conkey, 1) = 1
			  AND array_length(constraint_row.confkey, 1) = 1
			  AND local_attribute.attname = $2
			  AND target_attribute.attname = 'id'
			  AND constraint_row.convalidated
			  AND constraint_row.confdeltype = 'r'
			  AND constraint_row.confupdtype = 'a'
			  AND constraint_row.confmatchtype = 's'
			  AND NOT constraint_row.condeferrable
			  AND NOT constraint_row.condeferred`, table, column, targetTable).Scan(&count)
		return count == 1, err
	default:
		return false, fmt.Errorf("unsupported database type")
	}
}

func migrationColumnExists(db *sql.DB, dbType, table, column string) (bool, error) {
	var count int
	var err error
	switch dbType {
	case "sqlite":
		err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count)
	case "postgres":
		err = db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_attribute
			WHERE attrelid = pg_catalog.to_regclass($1)
			  AND attname = $2
			  AND NOT attisdropped`, table, column).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported database type")
	}
	return count == 1, err
}
func migrationColumnCount(db *sql.DB, dbType, table string) (int, error) {
	switch dbType {
	case "sqlite":
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?)", table).Scan(&count)
		return count, err
	case "postgres":
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_attribute
			WHERE attrelid = pg_catalog.to_regclass($1)
			  AND attnum > 0
			  AND NOT attisdropped`, table).Scan(&count)
		return count, err
	default:
		return 0, fmt.Errorf("unsupported database type")
	}
}

func migrationPrimaryKeyColumns(db *sql.DB, dbType, table string) ([]string, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch dbType {
	case "sqlite":
		rows, err = db.Query(`SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	case "postgres":
		rows, err = db.Query(`
			SELECT attribute.attname
			FROM pg_catalog.pg_index AS index_row
			JOIN pg_catalog.pg_class AS relation ON relation.oid = index_row.indrelid
			JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			JOIN LATERAL unnest(index_row.indkey::smallint[]) WITH ORDINALITY
			  AS primary_key(attnum, ordinal)
			  ON primary_key.ordinal <= index_row.indnkeyatts
			JOIN pg_catalog.pg_attribute AS attribute
			  ON attribute.attrelid = index_row.indrelid
			 AND attribute.attnum = primary_key.attnum
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND index_row.indisprimary
			ORDER BY primary_key.ordinal`, table)
	default:
		return nil, fmt.Errorf("unsupported database type")
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func migrationColumnContractOf(db *sql.DB, dbType, table, column string) (migrationColumnContract, error) {
	var contract migrationColumnContract
	var defaultSQL sql.NullString
	switch dbType {
	case "sqlite":
		var notNull int
		err := db.QueryRow(
			`SELECT type, "notnull", dflt_value FROM pragma_table_info(?) WHERE name = ?`,
			table,
			column,
		).Scan(&contract.dataType, &notNull, &defaultSQL)
		contract.notNull = notNull == 1
		if err != nil {
			return migrationColumnContract{}, err
		}
	case "postgres":
		var nullable string
		err := db.QueryRow(`
			SELECT data_type,
			       COALESCE(character_maximum_length, 0),
			       is_nullable,
			       column_default
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1
			  AND column_name = $2`, table, column).Scan(
			&contract.dataType,
			&contract.maxLength,
			&nullable,
			&defaultSQL,
		)
		contract.notNull = nullable == "NO"
		if err != nil {
			return migrationColumnContract{}, err
		}
	default:
		return migrationColumnContract{}, fmt.Errorf("unsupported database type")
	}
	contract.dataType = strings.ToLower(contract.dataType)
	if defaultSQL.Valid {
		contract.defaultSQL = defaultSQL.String
	}
	return contract, nil
}

func migrationIndexContractOf(db *sql.DB, dbType, table, index string) (migrationIndexContract, error) {
	var contract migrationIndexContract
	switch dbType {
	case "sqlite":
		contract.valid = true
		contract.ready = true
		contract.live = true
		var unique int
		var partial int
		if err := db.QueryRow(
			`SELECT "unique", partial FROM pragma_index_list(?) WHERE name = ?`,
			table,
			index,
		).Scan(&unique, &partial); err != nil {
			return migrationIndexContract{}, err
		}
		contract.unique = unique == 1
		var definition string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name = ?`,
			table,
			index,
		).Scan(&definition); err != nil {
			return migrationIndexContract{}, err
		}
		if partial == 1 {
			whereOffset := strings.Index(strings.ToLower(definition), "where")
			if whereOffset < 0 {
				return migrationIndexContract{}, errors.New("partial index has no predicate")
			}
			contract.predicate = definition[whereOffset+len("where"):]
		}
	case "postgres":
		if err := db.QueryRow(`
			SELECT index_row.indisunique,
			       index_row.indisvalid,
			       index_row.indisready,
			       index_row.indislive,
			       COALESCE(pg_catalog.pg_get_expr(index_row.indpred, index_row.indrelid), '')
			FROM pg_catalog.pg_index AS index_row
			JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_row.indexrelid
			WHERE index_row.indrelid = pg_catalog.to_regclass($1)
			  AND index_relation.relname = $2`, table, index).Scan(
			&contract.unique,
			&contract.valid,
			&contract.ready,
			&contract.live,
			&contract.predicate,
		); err != nil {
			return migrationIndexContract{}, err
		}
	default:
		return migrationIndexContract{}, fmt.Errorf("unsupported database type")
	}

	columns, err := migrationIndexColumns(db, dbType, table, index)
	if err != nil {
		return migrationIndexContract{}, err
	}
	contract.columns = columns
	return contract, nil
}

func migrationIndexColumns(db *sql.DB, dbType, table, index string) ([]string, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch dbType {
	case "sqlite":
		rows, err = db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	case "postgres":
		rows, err = db.Query(`
			SELECT attribute.attname
			FROM pg_catalog.pg_index AS index_row
			JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_row.indexrelid
			JOIN LATERAL unnest(index_row.indkey::smallint[]) WITH ORDINALITY
			  AS index_key(attnum, ordinal) ON index_key.ordinal <= index_row.indnkeyatts
			JOIN pg_catalog.pg_attribute AS attribute
			  ON attribute.attrelid = index_row.indrelid
			 AND attribute.attnum = index_key.attnum
			WHERE index_row.indrelid = pg_catalog.to_regclass($1)
			  AND index_relation.relname = $2
			ORDER BY index_key.ordinal`, table, index)
	default:
		return nil, fmt.Errorf("unsupported database type")
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func migrationRelationExists(db *sql.DB, dbType, name, kind string) (bool, error) {
	var count int
	var err error
	switch dbType {
	case "sqlite":
		err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", kind, name).Scan(&count)
	case "postgres":
		var expectedKind string
		switch kind {
		case "table":
			expectedKind = "r"
		case "index":
			expectedKind = "i"
		default:
			return false, fmt.Errorf("unsupported relation kind")
		}
		err = db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_class
			WHERE oid = pg_catalog.to_regclass($1)
			  AND relkind = $2`, name, expectedKind).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported database type")
	}
	return count == 1, err
}

func migrationCatalogIdentifier(dbType, name string) string {
	// PostgreSQL silently truncates unquoted identifiers to NAMEDATALEN-1
	// bytes. Catalog lookups must use the stored name for the few migration
	// objects whose declarative names exceed that limit.
	if dbType == "postgres" && len(name) > 63 {
		return name[:63]
	}
	return name
}

func migrationTriggerExists(db *sql.DB, dbType, table, trigger string) (bool, error) {
	var count int
	var err error
	trigger = migrationCatalogIdentifier(dbType, trigger)
	switch dbType {
	case "sqlite":
		err = db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? AND name = ?",
			table,
			trigger,
		).Scan(&count)
	case "postgres":
		err = db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_trigger
			WHERE tgrelid = pg_catalog.to_regclass($1)
			  AND tgname = $2
			  AND NOT tgisinternal`, table, trigger).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported database type")
	}
	return count == 1, err
}

func migrationTriggerEnabled(db *sql.DB, dbType, table, trigger string) (bool, error) {
	if dbType != "postgres" {
		if dbType == "sqlite" {
			return true, nil
		}
		return false, fmt.Errorf("unsupported database type")
	}
	trigger = migrationCatalogIdentifier(dbType, trigger)
	var enabled string
	err := db.QueryRow(`
		SELECT tgenabled
		FROM pg_catalog.pg_trigger
		WHERE tgrelid = pg_catalog.to_regclass($1)
		  AND tgname = $2
		  AND NOT tgisinternal`, table, trigger).Scan(&enabled)
	return enabled == "O", err
}

func migrationTriggerDefinition(db *sql.DB, dbType, table, trigger string) (string, error) {
	var definition string
	var err error
	trigger = migrationCatalogIdentifier(dbType, trigger)
	switch dbType {
	case "sqlite":
		err = db.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? AND name = ?",
			table,
			trigger,
		).Scan(&definition)
	case "postgres":
		err = db.QueryRow(`
			SELECT pg_catalog.pg_get_triggerdef(oid)
			FROM pg_catalog.pg_trigger
			WHERE tgrelid = pg_catalog.to_regclass($1)
			  AND tgname = $2
			  AND NOT tgisinternal`, table, trigger).Scan(&definition)
	default:
		return "", fmt.Errorf("unsupported database type")
	}
	return definition, err
}

func migrationTriggerFunctionDefinition(db *sql.DB, table, trigger string) (string, error) {
	var definition string
	trigger = migrationCatalogIdentifier("postgres", trigger)
	err := db.QueryRow(`
		SELECT pg_catalog.pg_get_functiondef(t.tgfoid)
		FROM pg_catalog.pg_trigger AS t
		WHERE t.tgrelid = pg_catalog.to_regclass($1)
		  AND t.tgname = $2
		  AND NOT t.tgisinternal`, table, trigger).Scan(&definition)
	return definition, err
}

func migrationConstraintExists(db *sql.DB, table, constraint string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_catalog.pg_constraint
		WHERE conrelid = pg_catalog.to_regclass($1)
		  AND conname = $2`, table, constraint).Scan(&count)
	return count == 1, err
}

func migrationConstraintDefinition(db *sql.DB, table, constraint string) (string, error) {
	var definition string
	err := db.QueryRow(`
		SELECT pg_catalog.pg_get_constraintdef(oid)
		FROM pg_catalog.pg_constraint
		WHERE conrelid = pg_catalog.to_regclass($1)
		  AND conname = $2`, table, constraint).Scan(&definition)
	return definition, err
}
