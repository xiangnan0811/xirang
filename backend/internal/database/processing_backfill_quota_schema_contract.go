package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const processingBackfillQuotaSchemaVersion int64 = 99

const processingBackfillQuotaPostgresBody = `BEGIN
    IF NEW.version < 99 THEN
        PERFORM 1 FROM backup_asset_processing_backfill_ledger WHERE id = 1 FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION '000099 downgrade blocked: ledger missing';
        END IF;
        IF EXISTS (SELECT 1 FROM backup_asset_processing_backfill_ledger WHERE jobs_count > 0 OR bytes_count > 0)
            OR EXISTS (SELECT 1 FROM backup_asset_processing_jobs WHERE backfill_charged) THEN
            RAISE EXCEPTION '000099 downgrade blocked: backfill quota used';
        END IF;
    END IF;
    RETURN NEW;
END;`

func preflightProcessingBackfillQuotaMigration(db *sql.DB, dbType string, version int64) error {
	if version >= processingBackfillQuotaSchemaVersion {
		return nil
	}
	collision := false
	for _, name := range []string{"backup_asset_processing_backfill_ledger", "idx_backup_asset_processing_jobs_backfill_active", "processing_000099_downgrade_blocked"} {
		var exists bool
		var err error
		if dbType == "sqlite" {
			err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name = ?)`, name).Scan(&exists)
		} else {
			err = db.QueryRow(`SELECT pg_catalog.to_regclass($1) IS NOT NULL`, name).Scan(&exists)
		}
		if err != nil {
			return migrationSchemaDriftError(version, "processing_backfill_quota_catalog_query_failed")
		}
		collision = collision || exists
	}
	jobs, err := migrationRelationExists(db, dbType, "backup_asset_processing_jobs", "table")
	if err != nil {
		return migrationSchemaDriftError(version, "processing_backfill_quota_catalog_query_failed")
	}
	if jobs {
		for _, column := range []string{"provider_kind", "estimated_input_bytes", "backfill_charged"} {
			exists, err := migrationColumnExists(db, dbType, "backup_asset_processing_jobs", column)
			if err != nil {
				return migrationSchemaDriftError(version, "processing_backfill_quota_catalog_query_failed")
			}
			collision = collision || exists
		}
	}
	if collision {
		return fmt.Errorf("%w (version=%d, reason=processing_backfill_quota_partial_schema)", ErrMigrationPrecondition, version)
	}
	if !jobs {
		return nil
	}
	var invalid bool
	err = db.QueryRow(`SELECT EXISTS (SELECT 1 FROM backup_asset_processing_jobs j
        WHERE (SELECT count(*) FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id
               WHERE p.id = j.recovery_point_id) <> 1
           OR NOT EXISTS (SELECT 1 FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id
               WHERE p.id = j.recovery_point_id AND lower(trim(r.provider_kind)) IN ('restic','rsync','rclone','command'))
           OR (SELECT count(*) FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id
               AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id) <> 1
           OR NOT EXISTS (SELECT 1 FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id
               AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id AND e.size >= 0))`).Scan(&invalid)
	if err != nil {
		return migrationSchemaDriftError(version, "processing_backfill_quota_source_query_failed")
	}
	if invalid {
		return fmt.Errorf("%w (version=%d, reason=processing_backfill_quota_invalid_source)", ErrMigrationPrecondition, version)
	}
	return nil
}

func validateProcessingBackfillQuotaSchema(db *sql.DB, dbType string) error {
	const jobs = "backup_asset_processing_jobs"
	const ledger = "backup_asset_processing_backfill_ledger"
	columns := []struct {
		table, name, sqlite, postgres, defaultValue string
		length                                      int64
	}{
		{jobs, "provider_kind", "text", "character varying", "''", 32},
		{jobs, "estimated_input_bytes", "integer", "bigint", "0", 0},
		{jobs, "backfill_charged", "integer", "boolean", "0", 0},
		{ledger, "id", "integer", "integer", "", 0},
		{ledger, "window_started_at", "datetime", "timestamp with time zone", "", 0},
		{ledger, "jobs_count", "integer", "bigint", "0", 0},
		{ledger, "bytes_count", "integer", "bigint", "0", 0},
		{ledger, "updated_at", "datetime", "timestamp with time zone", "", 0},
	}
	if n, err := migrationColumnCount(db, dbType, ledger); err != nil || n != 5 {
		return errors.New("invalid_processing_backfill_ledger_columns")
	}
	for _, expected := range columns {
		col, err := migrationColumnContractOf(db, dbType, expected.table, expected.name)
		typ, def := expected.sqlite, expected.defaultValue
		if dbType == "postgres" {
			typ = expected.postgres
			if expected.name == "backfill_charged" {
				def = "false"
			}
		}
		got := normalizeMigrationSQLToken(col.defaultSQL)
		got = strings.TrimSuffix(got, "::charactervarying")
		if err != nil || !col.notNull || col.dataType != typ || (dbType == "postgres" && expected.length > 0 && col.maxLength != expected.length) || got != def {
			return errors.New("invalid_processing_backfill_quota_column")
		}
	}
	pk, err := migrationPrimaryKeyColumns(db, dbType, ledger)
	if err != nil || !sameMigrationIndexColumns(pk, []string{"id"}) {
		return errors.New("invalid_processing_backfill_ledger_primary_key")
	}
	index, err := migrationIndexContractOf(db, dbType, jobs, "idx_backup_asset_processing_jobs_backfill_active")
	if err != nil || !migrationIndexUsable(index) || index.unique || index.predicate != "" || !sameMigrationIndexColumns(index.columns, []string{"is_current", "priority_class", "state", "provider_kind", "capability"}) {
		return errors.New("invalid_processing_backfill_quota_index")
	}
	if dbType == "postgres" {
		var exact bool
		var definition string
		err := db.QueryRow(`SELECT i.indnatts = 5 AND i.indnkeyatts = 5 AND am.amname = 'btree'
			AND NOT EXISTS (
				SELECT 1 FROM generate_series(0, 4) AS k(n)
				LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[k.n]
				LEFT JOIN pg_catalog.pg_opclass op ON op.oid = i.indclass[k.n]
				WHERE a.attnum IS NULL OR i.indoption[k.n] <> 0 OR i.indcollation[k.n] <> a.attcollation
				   OR op.oid IS NULL OR NOT op.opcdefault OR op.opcmethod <> c.relam
			), pg_catalog.pg_get_indexdef(i.indexrelid)
			FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
			JOIN pg_catalog.pg_am am ON am.oid = c.relam
			WHERE i.indrelid = pg_catalog.to_regclass($1) AND c.relname = $2`, jobs, "idx_backup_asset_processing_jobs_backfill_active").Scan(&exact, &definition)
		const indexDefinition = `USING btree (is_current, priority_class, state, provider_kind, capability)`
		if err != nil || !exact || !strings.HasSuffix(normalizeMigrationGuardText(definition), normalizeMigrationGuardText(indexDefinition)) {
			return errors.New("invalid_processing_backfill_quota_index")
		}
	} else {
		var definition string
		err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_backup_asset_processing_jobs_backfill_active'`).Scan(&definition)
		if err != nil || normalizeMigrationGuardText(definition) != normalizeMigrationGuardText(`CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind, capability)`) {
			return errors.New("invalid_processing_backfill_quota_index")
		}
	}
	for _, spec := range []struct {
		table string
		want  []string
	}{
		{jobs, []string{"estimated_input_bytes>=0"}},
		{ledger, []string{"id=1", "jobs_count>=0", "bytes_count>=0"}},
	} {
		checks, err := migrationTableCheckDefinitions(db, dbType, spec.table)
		if err != nil {
			return errors.New("invalid_processing_backfill_quota_checks")
		}
		wants := append([]string(nil), spec.want...)
		if spec.table == jobs {
			if dbType == "sqlite" {
				wants = append(wants, "provider_kindin'','restic','rsync','rclone','command'", "backfill_chargedin0,1")
			} else {
				wants = append(wants, "provider_kind=anyarray['','restic','rsync','rclone','command']")
			}
		}
		matched := make(map[string]int, len(wants))
		for _, check := range checks {
			expression := normalizeMigrationCheckExpression(dbType, check.expression)
			relevant := spec.table == ledger || strings.Contains(expression, "provider_kind") || strings.Contains(expression, "estimated_input_bytes") || strings.Contains(expression, "backfill_charged")
			if !relevant {
				continue
			}
			if !check.validated {
				return errors.New("invalid_processing_backfill_quota_checks")
			}
			matched[expression]++
		}
		if len(matched) != len(wants) {
			return errors.New("invalid_processing_backfill_quota_checks")
		}
		for _, want := range wants {
			if matched[want] != 1 {
				return errors.New("invalid_processing_backfill_quota_checks")
			}
		}
	}
	var id, count int
	var window, updated time.Time
	var jobsCount, bytesCount int64
	rows, err := db.Query(`SELECT id, window_started_at, jobs_count, bytes_count, updated_at FROM backup_asset_processing_backfill_ledger`)
	if err != nil {
		return errors.New("invalid_processing_backfill_ledger")
	}
	defer rows.Close()
	for rows.Next() {
		count++
		if err := rows.Scan(&id, &window, &jobsCount, &bytesCount, &updated); err != nil {
			return errors.New("invalid_processing_backfill_ledger")
		}
	}
	_, windowOffset := window.Zone()
	_, updatedOffset := updated.Zone()
	if rows.Err() != nil || count != 1 || id != 1 || jobsCount < 0 || bytesCount < 0 || window.IsZero() || updated.IsZero() || (dbType == "sqlite" && (windowOffset != 0 || updatedOffset != 0)) || window.UTC().Minute() != 0 || window.UTC().Second() != 0 || window.Nanosecond() != 0 {
		return errors.New("invalid_processing_backfill_ledger")
	}
	if err := rows.Close(); err != nil {
		return errors.New("invalid_processing_backfill_ledger")
	}
	if dbType == "sqlite" {
		var rawWindow, rawUpdated string
		if err := db.QueryRow(`SELECT CAST(window_started_at AS TEXT), CAST(updated_at AS TEXT) FROM backup_asset_processing_backfill_ledger WHERE id = 1`).Scan(&rawWindow, &rawUpdated); err != nil {
			return errors.New("invalid_processing_backfill_ledger")
		}
		utc := func(value string) bool {
			return strings.HasSuffix(value, "Z") || strings.HasSuffix(value, "+00:00") || strings.HasSuffix(value, "+0000")
		}
		if !utc(rawWindow) || !utc(rawUpdated) {
			return errors.New("invalid_processing_backfill_ledger")
		}
	}
	return validateProcessingBackfillQuotaGuards(db, dbType)
}

func validateProcessingBackfillQuotaGuards(db *sql.DB, dbType string) error {
	const trigger = "trg_processing_backfill_quota_downgrade_admission"
	if dbType == "postgres" {
		c := lifecycleEffectClaimAuditSlotTriggerContract{table: "schema_migrations", name: trigger, triggerFragments: []string{"BEFORE INSERT OR UPDATE"}, postgresFunctionName: "processing_backfill_quota_downgrade_admission", postgresFunctionBody: processingBackfillQuotaPostgresBody}
		definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", trigger)
		enabled, e := migrationTriggerEnabled(db, dbType, "schema_migrations", trigger)
		function, f := migrationTriggerFunctionDefinition(db, "schema_migrations", trigger)
		if err != nil || e != nil || f != nil || !enabled || !lifecyclePostgresGuardDefinitionExact(definition, c) || !lifecyclePostgresGuardFunctionDefinitionExact(function, c) {
			return errors.New("invalid_processing_backfill_quota_guard")
		}
		return nil
	}
	script, err := sqliteMigrationsFS.ReadFile("migrations/sqlite/000099_processing_backfill_quota.up.sql")
	if err != nil {
		return errors.New("invalid_processing_backfill_quota_guard")
	}
	text := string(script)
	for _, name := range []string{trigger, "trg_processing_backfill_quota_downgrade_update_admission"} {
		actual, err := migrationTriggerDefinition(db, dbType, "schema_migrations", name)
		start := strings.Index(text, "CREATE TRIGGER "+name+" ")
		if err != nil || start < 0 {
			return errors.New("invalid_processing_backfill_quota_guard")
		}
		end := strings.Index(text[start:], "\nEND;")
		if end < 0 || strings.TrimSuffix(normalizeMigrationGuardText(actual), ";") != strings.TrimSuffix(normalizeMigrationGuardText(text[start:start+end+len("\nEND;")]), ";") {
			return errors.New("invalid_processing_backfill_quota_guard")
		}
	}
	var actual string
	err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'view' AND name = 'processing_000099_downgrade_blocked'`).Scan(&actual)
	start := strings.Index(text, "CREATE VIEW processing_000099_downgrade_blocked AS")
	end := strings.Index(text, "\nCREATE TRIGGER "+trigger)
	if err != nil || start < 0 || end <= start || strings.TrimSuffix(normalizeMigrationGuardText(actual), ";") != strings.TrimSuffix(normalizeMigrationGuardText(text[start:end]), ";") {
		return errors.New("invalid_processing_backfill_quota_guard")
	}
	return nil
}
