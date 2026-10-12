package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

const batchCommandRetrySchemaVersion int64 = 93

const (
	batchCommandRetryDowngradeAdmissionTrigger       = "trg_batch_command_retry_downgrade_admission"
	batchCommandRetryDowngradeUpdateAdmissionTrigger = "trg_batch_command_retry_downgrade_update_admission"
	batchCommandRetryDowngradeAdmissionFunction      = "batch_command_retry_downgrade_admission"
	batchCommandRetryBatchGuardFunction              = "batch_command_retry_batch_guard"
	batchCommandRetryDispatchGuardFunction           = "batch_command_retry_dispatch_guard"
	batchCommandRetryEvidenceGuardFunction           = "batch_command_retry_evidence_guard"
	batchCommandRetryDispatchSourceIndex             = "idx_batch_command_dispatches_retry_source"
	batchCommandRetryEvidenceTaskRunIndex            = "idx_batch_command_run_evidences_task_run"
)

// validateBatchCommandRetrySchema verifies the actual v1 retry schema rather
// than trusting migration version metadata or object names. It deliberately
// accepts legacy v0 rows while requiring every v1 boundary and guard to remain
// fail-closed after restores or operator-side table rebuilds.
func validateBatchCommandRetrySchema(db *sql.DB, dbType string) error {
	for _, table := range []string{
		"batch_commands",
		"batch_command_dispatches",
		"batch_command_run_evidences",
	} {
		exists, err := migrationRelationExists(db, dbType, table, "table")
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !exists {
			return errors.New("missing_batch_command_retry_table")
		}
	}
	if err := validateBatchCommandRetryColumns(db, dbType); err != nil {
		return err
	}
	if err := validateBatchCommandRetryPrimaryKeys(db, dbType); err != nil {
		return err
	}
	if err := validateBatchCommandRetryIndexes(db, dbType); err != nil {
		return err
	}
	if err := validateBatchCommandRetryForeignKeys(db, dbType); err != nil {
		return err
	}
	if err := validateBatchCommandRetryChecks(db, dbType); err != nil {
		return err
	}
	if err := validateBatchCommandRetryGuards(db, dbType); err != nil {
		return err
	}
	return nil
}

type batchCommandRetryColumnContract struct {
	name         string
	sqliteType   string
	postgresType string
	maxLength    int64
	notNull      bool
	defaultSQL   string
}

func validateBatchCommandRetryColumns(db *sql.DB, dbType string) error {
	contracts := map[string][]batchCommandRetryColumnContract{
		"batch_commands": {
			{name: "provenance_version", sqliteType: "integer", postgresType: "integer", notNull: true, defaultSQL: "0"},
			{name: "source_batch_id", sqliteType: "varchar(64)", postgresType: "character varying", maxLength: 64},
		},
		"batch_command_dispatches": {
			{name: "command_fingerprint", sqliteType: "varchar(64)", postgresType: "character varying", maxLength: 64, notNull: true, defaultSQL: "''"},
			{name: "retry_of_task_id", sqliteType: "integer", postgresType: "bigint"},
			{name: "retry_of_run_id", sqliteType: "integer", postgresType: "bigint"},
		},
		"batch_command_run_evidences": {
			{name: "run_id", sqliteType: "integer", postgresType: "bigint", notNull: true},
			{name: "task_id", sqliteType: "integer", postgresType: "bigint", notNull: true},
			{name: "node_id", sqliteType: "integer", postgresType: "bigint", notNull: true},
			{name: "command_fingerprint", sqliteType: "varchar(64)", postgresType: "character varying", maxLength: 64, notNull: true, defaultSQL: "''"},
			{name: "outcome", sqliteType: "varchar(16)", postgresType: "character varying", maxLength: 16, notNull: true, defaultSQL: "'pending'"},
			{name: "exit_code", sqliteType: "integer", postgresType: "integer"},
			{name: "finished_at", sqliteType: "datetime", postgresType: "timestamp with time zone"},
			{name: "created_at", sqliteType: "datetime", postgresType: "timestamp with time zone", notNull: true},
		},
	}
	for table, tableContracts := range contracts {
		for _, want := range tableContracts {
			got, err := migrationColumnContractOf(db, dbType, table, want.name)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("missing_batch_command_retry_column")
				}
				return errors.New("catalog_query_failed")
			}
			typeWant := want.sqliteType
			if dbType == "postgres" {
				typeWant = want.postgresType
				if want.maxLength > 0 && got.maxLength != want.maxLength {
					return errors.New("invalid_batch_command_retry_column")
				}
			}
			if got.dataType != typeWant || got.notNull != want.notNull ||
				!batchCommandRetryDefaultEqual(dbType, got.defaultSQL, want.defaultSQL) {
				return errors.New("invalid_batch_command_retry_column")
			}
		}
	}
	for table, count := range map[string]int{
		"batch_commands":              10,
		"batch_command_dispatches":    11,
		"batch_command_run_evidences": 8,
	} {
		got, err := migrationColumnCount(db, dbType, table)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if got != count {
			return errors.New("invalid_batch_command_retry_columns")
		}
	}
	return nil
}

func batchCommandRetryDefaultEqual(dbType, got, want string) bool {
	got = normalizeMigrationSQLToken(got)
	want = normalizeMigrationSQLToken(want)
	if dbType == "postgres" {
		for _, suffix := range []string{
			"::charactervarying",
			"::integer",
			"::bigint",
			"::text",
		} {
			got = strings.TrimSuffix(got, suffix)
		}
	}
	return got == want
}

func validateBatchCommandRetryPrimaryKeys(db *sql.DB, dbType string) error {
	for table, want := range map[string][]string{
		"batch_commands":              {"id"},
		"batch_command_dispatches":    {"task_id"},
		"batch_command_run_evidences": {"run_id"},
	} {
		got, err := migrationPrimaryKeyColumns(db, dbType, table)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !sameMigrationIndexColumns(got, want) {
			return errors.New("invalid_batch_command_retry_primary_key")
		}
	}
	return nil
}

func validateBatchCommandRetryIndexes(db *sql.DB, dbType string) error {
	retrySource, err := migrationIndexContractOf(db, dbType, "batch_command_dispatches", batchCommandRetryDispatchSourceIndex)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_batch_command_retry_source_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(retrySource) || !retrySource.unique ||
		!sameMigrationIndexColumns(retrySource.columns, []string{"retry_of_task_id", "retry_of_run_id"}) ||
		strings.TrimSpace(retrySource.predicate) != "" {
		return errors.New("invalid_batch_command_retry_source_index")
	}
	evidenceIndex, err := migrationIndexContractOf(db, dbType, "batch_command_run_evidences", batchCommandRetryEvidenceTaskRunIndex)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_batch_command_retry_evidence_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(evidenceIndex) || evidenceIndex.unique ||
		!sameMigrationIndexColumns(evidenceIndex.columns, []string{"task_id", "run_id"}) ||
		strings.TrimSpace(evidenceIndex.predicate) != "" {
		return errors.New("invalid_batch_command_retry_evidence_index")
	}
	return nil
}

func validateBatchCommandRetryForeignKeys(db *sql.DB, dbType string) error {
	counts := map[string]int{
		"batch_commands":              1,
		"batch_command_dispatches":    2,
		"batch_command_run_evidences": 1,
	}
	for table, want := range counts {
		got, err := migrationForeignKeyCount(db, dbType, table)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if got != want {
			return errors.New("invalid_batch_command_retry_foreign_key_count")
		}
	}
	checks := []struct {
		table, column, target, targetColumn, onDelete string
	}{
		{table: "batch_commands", column: "source_batch_id", target: "batch_commands", targetColumn: "id", onDelete: "RESTRICT"},
		{table: "batch_command_dispatches", column: "batch_id", target: "batch_commands", targetColumn: "id", onDelete: "NO ACTION"},
		{table: "batch_command_dispatches", column: "retry_of_task_id", target: "batch_command_dispatches", targetColumn: "task_id", onDelete: "RESTRICT"},
		{table: "batch_command_run_evidences", column: "task_id", target: "batch_command_dispatches", targetColumn: "task_id", onDelete: "RESTRICT"},
	}
	for _, want := range checks {
		ok, err := batchCommandRetryForeignKeyExists(db, dbType, want.table, want.column, want.target, want.targetColumn, want.onDelete)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !ok {
			return errors.New("invalid_batch_command_retry_foreign_key")
		}
	}
	return nil
}

func batchCommandRetryForeignKeyExists(db *sql.DB, dbType, table, column, targetTable, targetColumn, onDelete string) (bool, error) {
	switch dbType {
	case "sqlite":
		rows, err := db.Query("PRAGMA foreign_key_list('" + table + "')")
		if err != nil {
			return false, err
		}
		defer func() { _ = rows.Close() }()
		matches := 0
		for rows.Next() {
			var id, seq int
			var target, from, to, onUpdate, gotDelete, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &gotDelete, &match); err != nil {
				return false, err
			}
			if seq == 0 && from == column && target == targetTable && to == targetColumn &&
				strings.EqualFold(onUpdate, "NO ACTION") && strings.EqualFold(gotDelete, onDelete) &&
				strings.EqualFold(match, "NONE") {
				matches++
			}
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		return matches == 1, nil
	case "postgres":
		wantDelete := "a"
		if strings.EqualFold(onDelete, "RESTRICT") {
			wantDelete = "r"
		}
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_constraint AS constraint_row
			JOIN pg_catalog.pg_class AS relation ON relation.oid = constraint_row.conrelid
			JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			JOIN pg_catalog.pg_class AS target_relation ON target_relation.oid = constraint_row.confrelid
			JOIN pg_catalog.pg_namespace AS target_namespace ON target_namespace.oid = target_relation.relnamespace
			JOIN LATERAL unnest(constraint_row.conkey) WITH ORDINALITY AS local_key(attnum, ordinal) ON TRUE
			JOIN LATERAL unnest(constraint_row.confkey) WITH ORDINALITY AS target_key(attnum, ordinal)
			  ON target_key.ordinal = local_key.ordinal
			JOIN pg_catalog.pg_attribute AS local_attribute
			  ON local_attribute.attrelid = relation.oid AND local_attribute.attnum = local_key.attnum
			JOIN pg_catalog.pg_attribute AS target_attribute
			  ON target_attribute.attrelid = target_relation.oid AND target_attribute.attnum = target_key.attnum
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND target_namespace.nspname = current_schema()
			  AND target_relation.relname = $3
			  AND constraint_row.contype = 'f'
			  AND array_length(constraint_row.conkey, 1) = 1
			  AND array_length(constraint_row.confkey, 1) = 1
			  AND local_attribute.attname = $2
			  AND target_attribute.attname = $4
			  AND constraint_row.convalidated
			  AND constraint_row.confdeltype = $5
			  AND constraint_row.confupdtype = 'a'
			  AND constraint_row.confmatchtype = 's'
			  AND NOT constraint_row.condeferrable
			  AND NOT constraint_row.condeferred`, table, column, targetTable, targetColumn, wantDelete).Scan(&count)
		return count == 1, err
	default:
		return false, errors.New("unsupported_database_type")
	}
}

func validateBatchCommandRetryChecks(db *sql.DB, dbType string) error {
	checks := map[string][]string{
		"batch_commands": {
			"provenance_version IN (0, 1)",
		},
		"batch_command_dispatches": {
			"status IN ('pending', 'dispatching', 'accepted', 'failed')",
			"command_fingerprint = '' OR (length(command_fingerprint) = 64 AND command_fingerprint NOT GLOB '*[^0-9a-f]*')",
			"retry_of_task_id IS NULL OR retry_of_task_id > 0",
			"((retry_of_task_id IS NULL AND retry_of_run_id IS NULL) OR (retry_of_task_id IS NOT NULL AND retry_of_run_id IS NOT NULL)) AND (retry_of_run_id IS NULL OR retry_of_run_id >= 0)",
		},
		"batch_command_run_evidences": {
			"run_id > 0",
			"task_id > 0",
			"node_id > 0",
			"command_fingerprint = '' OR (length(command_fingerprint) = 64 AND command_fingerprint NOT GLOB '*[^0-9a-f]*')",
			"outcome IN ('pending', 'exit_failure', 'not_started', 'success', 'unknown')",
			"outcome = 'exit_failure' AND exit_code IS NOT NULL AND exit_code > 0 OR outcome <> 'exit_failure' AND exit_code IS NULL",
			"outcome = 'pending' AND finished_at IS NULL OR outcome <> 'pending' AND finished_at IS NOT NULL",
			"outcome IN ('pending', 'unknown', 'success') OR command_fingerprint <> ''",
		},
	}
	if dbType == "postgres" {
		checks["batch_commands"] = []string{
			"provenance_version = ANY (ARRAY[0, 1])",
		}
		checks["batch_command_dispatches"] = []string{
			"status = ANY (ARRAY['pending', 'dispatching', 'accepted', 'failed'])",
			"command_fingerprint = '' OR command_fingerprint ~ '^[0-9a-f]{64}$'",
			"(retry_of_task_id IS NULL AND retry_of_run_id IS NULL) OR (retry_of_task_id IS NOT NULL AND retry_of_run_id IS NOT NULL)",
			"retry_of_task_id IS NULL OR retry_of_task_id > 0",
			"retry_of_run_id IS NULL OR retry_of_run_id >= 0",
		}
		checks["batch_command_run_evidences"] = []string{
			"run_id > 0",
			"task_id > 0",
			"node_id > 0",
			"command_fingerprint = '' OR command_fingerprint ~ '^[0-9a-f]{64}$'",
			"outcome = ANY (ARRAY['pending', 'exit_failure', 'not_started', 'success', 'unknown'])",
			"outcome = 'exit_failure' AND exit_code IS NOT NULL AND exit_code > 0 OR outcome <> 'exit_failure' AND exit_code IS NULL",
			"outcome = 'pending' AND finished_at IS NULL OR outcome <> 'pending' AND finished_at IS NOT NULL",
			"outcome = ANY (ARRAY['pending', 'unknown', 'success']) OR command_fingerprint <> ''",
		}
	}
	for table, wants := range checks {
		got, err := migrationTableCheckDefinitions(db, dbType, table)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !migrationCheckMultisetExact(dbType, got, wants) {
			return errors.New("invalid_batch_command_retry_check")
		}
	}
	return nil
}

func validateBatchCommandRetryGuards(db *sql.DB, dbType string) error {
	if dbType == "sqlite" {
		for _, guard := range []struct {
			name, table string
		}{
			{name: "trg_batch_command_retry_batch_insert", table: "batch_commands"},
			{name: "trg_batch_command_retry_batch_update", table: "batch_commands"},
			{name: "trg_batch_command_retry_batch_delete", table: "batch_commands"},
			{name: "trg_batch_command_retry_dispatch_insert", table: "batch_command_dispatches"},
			{name: "trg_batch_command_retry_dispatch_update", table: "batch_command_dispatches"},
			{name: "trg_batch_command_retry_dispatch_delete", table: "batch_command_dispatches"},
			{name: "trg_batch_command_retry_evidence_insert", table: "batch_command_run_evidences"},
			{name: "trg_batch_command_retry_evidence_update", table: "batch_command_run_evidences"},
			{name: "trg_batch_command_retry_evidence_delete", table: "batch_command_run_evidences"},
			{name: batchCommandRetryDowngradeAdmissionTrigger, table: "schema_migrations"},
			{name: batchCommandRetryDowngradeUpdateAdmissionTrigger, table: "schema_migrations"},
		} {
			definition, err := migrationTriggerDefinition(db, dbType, guard.table, guard.name)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("missing_batch_command_retry_guard")
				}
				return errors.New("catalog_query_failed")
			}
			expected, ok := batchCommandRetrySQLiteGuardDigest(guard.name)
			actual := batchCommandRetryDigest(strings.TrimSuffix(normalizeMigrationDefinition(definition), ";"))
			if !ok || actual != expected {
				return errors.New("invalid_batch_command_retry_guard")
			}
		}
		return nil
	}
	if dbType != "postgres" {
		return errors.New("catalog_query_failed")
	}

	for _, guard := range []struct {
		name, table, event, function string
	}{
		{name: "trg_batch_command_retry_batch_insert", table: "batch_commands", event: "BEFORE INSERT", function: batchCommandRetryBatchGuardFunction},
		{name: "trg_batch_command_retry_batch_update", table: "batch_commands", event: "BEFORE UPDATE", function: batchCommandRetryBatchGuardFunction},
		{name: "trg_batch_command_retry_batch_delete", table: "batch_commands", event: "BEFORE DELETE", function: batchCommandRetryBatchGuardFunction},
		{name: "trg_batch_command_retry_dispatch_insert", table: "batch_command_dispatches", event: "BEFORE INSERT", function: batchCommandRetryDispatchGuardFunction},
		{name: "trg_batch_command_retry_dispatch_update", table: "batch_command_dispatches", event: "BEFORE UPDATE", function: batchCommandRetryDispatchGuardFunction},
		{name: "trg_batch_command_retry_dispatch_delete", table: "batch_command_dispatches", event: "BEFORE DELETE", function: batchCommandRetryDispatchGuardFunction},
		{name: "trg_batch_command_retry_evidence_insert", table: "batch_command_run_evidences", event: "BEFORE INSERT", function: batchCommandRetryEvidenceGuardFunction},
		{name: "trg_batch_command_retry_evidence_update", table: "batch_command_run_evidences", event: "BEFORE UPDATE", function: batchCommandRetryEvidenceGuardFunction},
		{name: "trg_batch_command_retry_evidence_delete", table: "batch_command_run_evidences", event: "BEFORE DELETE", function: batchCommandRetryEvidenceGuardFunction},
	} {
		definition, err := migrationTriggerDefinition(db, dbType, guard.table, guard.name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("missing_batch_command_retry_guard")
			}
			return errors.New("catalog_query_failed")
		}
		enabled, err := migrationTriggerEnabled(db, dbType, guard.table, guard.name)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !enabled || !lifecyclePostgresGuardDefinitionExact(definition, lifecycleEffectClaimAuditSlotTriggerContract{
			table: guard.table, name: guard.name, triggerFragments: []string{guard.event}, postgresFunctionName: guard.function,
		}) {
			return errors.New("invalid_batch_command_retry_guard")
		}
		functionDefinition, err := migrationTriggerFunctionDefinition(db, guard.table, guard.name)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		actualBody, ok := migrationPostgresFunctionBody(functionDefinition)
		expected, expectedOK := batchCommandRetryPostgresFunctionDigest(guard.function)
		if !ok || !expectedOK || batchCommandRetryDigest(normalizeMigrationGuardBody(actualBody)) != expected {
			return errors.New("invalid_batch_command_retry_guard")
		}
	}

	admissionDefinition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", batchCommandRetryDowngradeAdmissionTrigger)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_batch_command_retry_admission_trigger")
		}
		return errors.New("catalog_query_failed")
	}
	enabled, err := migrationTriggerEnabled(db, dbType, "schema_migrations", batchCommandRetryDowngradeAdmissionTrigger)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if !enabled || !lifecyclePostgresGuardDefinitionExact(admissionDefinition, lifecycleEffectClaimAuditSlotTriggerContract{
		table: "schema_migrations", name: batchCommandRetryDowngradeAdmissionTrigger,
		triggerFragments: []string{"BEFORE INSERT OR UPDATE"}, postgresFunctionName: batchCommandRetryDowngradeAdmissionFunction,
	}) {
		return errors.New("invalid_batch_command_retry_admission_trigger")
	}
	functionDefinition, err := migrationTriggerFunctionDefinition(db, "schema_migrations", batchCommandRetryDowngradeAdmissionTrigger)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	actualBody, ok := migrationPostgresFunctionBody(functionDefinition)
	expected, expectedOK := batchCommandRetryPostgresFunctionDigest(batchCommandRetryDowngradeAdmissionFunction)
	if !ok || !expectedOK || batchCommandRetryDigest(normalizeMigrationGuardBody(actualBody)) != expected {
		return errors.New("invalid_batch_command_retry_admission_trigger")
	}
	return nil
}

func batchCommandRetryDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// These digests pin complete canonical guard definitions independently of the
// embedded migration source. SQLite uses normalizeMigrationDefinition; PostgreSQL
// pins normalizeMigrationGuardBody of pg_get_functiondef's extracted body, while
// trigger events, attachment and enabled state are checked separately above.
func batchCommandRetrySQLiteGuardDigest(name string) (string, bool) {
	switch name {
	case "trg_batch_command_retry_batch_insert":
		return "2abd1528650be0a437248ea663e94fad696762b89b2b5d1f6a413e04cd5d0fb9", true
	case "trg_batch_command_retry_batch_update":
		return "157a2c7cf47058d1f6ead98ca830d99d23678d5ab76df84490fec79d6dd09085", true
	case "trg_batch_command_retry_batch_delete":
		return "5bb835a2dab337de05122f6f66084a346799eb8ee0ad69c5cdf6f158dda91cc9", true
	case "trg_batch_command_retry_dispatch_insert":
		return "ef8ba4524c9a189054e19e11261193395bbda7976dc25f64cd4742ee8ce60094", true
	case "trg_batch_command_retry_dispatch_update":
		return "fa1ba2276c87be3783a3e30b7b493ae1c6491243e4aee6ff58e952c6211c0473", true
	case "trg_batch_command_retry_dispatch_delete":
		return "53201377aa7bca7e4cf5b9da445b5d5638d9273ab329e4325735a8ec2f03f822", true
	case "trg_batch_command_retry_evidence_insert":
		return "25b1e4ead7b6c54bf00211ace6ea91f27545831071fe0c12aaf58d0f24bf6e12", true
	case "trg_batch_command_retry_evidence_update":
		return "3916c237f51101c62c55493d663d5dde5febd012220329a7f792b44e4534fb2f", true
	case "trg_batch_command_retry_evidence_delete":
		return "47d94f02deb1fcaf78d4b5369af44d2bb3ae82a58289c00a50044b5eea52f3ed", true
	case batchCommandRetryDowngradeAdmissionTrigger:
		return "b8714d605e58f8f3163f92b03f4faa86f9ed794e627986607e7e0da40ddb8357", true
	case batchCommandRetryDowngradeUpdateAdmissionTrigger:
		return "a35a8483efaa1da260dd8bd6e796012f77f587f4778776650dd75e2421505e04", true
	default:
		return "", false
	}
}

func batchCommandRetryPostgresFunctionDigest(name string) (string, bool) {
	switch name {
	case batchCommandRetryBatchGuardFunction:
		return "6269aa5af9fa823207fd47f1a690851ad426355021dea237156c86cf90ee740a", true
	case batchCommandRetryDispatchGuardFunction:
		return "4f8da1e355807afbb93e7851828889f1557140196d8f99ef458e79f21e4d9763", true
	case batchCommandRetryEvidenceGuardFunction:
		return "adcdf362922447431df815252247eff89c877caa41b4cf2c2514bb081740a826", true
	case batchCommandRetryDowngradeAdmissionFunction:
		return "1c509f515f41fc496e9d5642e5de3dc5e8b8bb53102185377e1d7a26ea910a6d", true
	default:
		return "", false
	}
}
