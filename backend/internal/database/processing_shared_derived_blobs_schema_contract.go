package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const processingSharedDerivedBlobsSchemaVersion int64 = 98

const (
	processingSharedDerivedBlobsTrigger       = "trg_processing_shared_derived_blobs_downgrade_admission"
	processingSharedDerivedBlobsUpdateTrigger = "trg_processing_shared_derived_blobs_downgrade_update_admission"
	processingSharedDerivedBlobsFunction      = "processing_shared_derived_blobs_downgrade_admission"
	processingSharedDerivedBlobsPostgresBody  = `BEGIN
    IF NEW.version < 98 AND (EXISTS (SELECT 1 FROM backup_asset_processing_uploads GROUP BY staging_id HAVING COUNT(*) > 1)
        OR EXISTS (SELECT 1 FROM backup_asset_derived_artifact_sets GROUP BY manifest_digest HAVING COUNT(*) > 1)) THEN
        RAISE EXCEPTION '000098 downgrade blocked: shared derived staging_id or manifest_digest exists';
    END IF;
    RETURN NEW;
END;`
)

// The migration's embedded SQL is the canonical definition of the SQLite guard.
// Reading that same predicate for preflight keeps its allowlist in sync with the
// independent SQL guard that runs when a migration is applied directly.
func processingSharedDerivedBlobsSQLiteUpgrade() (string, error) {
	data, err := sqliteMigrationsFS.ReadFile("migrations/sqlite/000098_processing_shared_derived_blobs.up.sql")
	return string(data), err
}

func preflightProcessingSharedDerivedBlobsMigration(db *sql.DB, dbType string, version int64) error {
	if dbType != "sqlite" || version >= processingSharedDerivedBlobsSchemaVersion {
		return nil
	}
	script, err := processingSharedDerivedBlobsSQLiteUpgrade()
	if err != nil {
		return migrationSchemaDriftError(version, "processing_shared_derived_blobs_guard_unavailable")
	}
	start := strings.Index(script, "SELECT CASE WHEN")
	end := strings.Index(script, "THEN 0 ELSE 1 END;")
	if start < 0 || end < start {
		return migrationSchemaDriftError(version, "processing_shared_derived_blobs_guard_invalid")
	}
	query := script[start : end+len("THEN 0 ELSE 1 END")]
	var valid int
	if err := db.QueryRow(query).Scan(&valid); err != nil {
		return migrationSchemaDriftError(version, "processing_shared_derived_blobs_schema_query_failed")
	}
	if valid != 1 {
		return fmt.Errorf("%w (version=%d, reason=unexpected_processing_shared_derived_blobs_sqlite_object)", ErrMigrationPrecondition, version)
	}
	return nil
}

func validateProcessingSharedDerivedBlobsSchema(db *sql.DB, dbType string) error {
	for _, expected := range []struct {
		name    string
		columns []string
	}{
		{"idx_backup_asset_processing_uploads_staging_id", []string{"staging_id", "state"}},
		{"idx_backup_asset_processing_uploads_staging", []string{"state", "updated_at"}},
	} {
		index, err := migrationIndexContractOf(db, dbType, "backup_asset_processing_uploads", expected.name)
		if err != nil || !migrationIndexUsable(index) || index.unique || index.predicate != "" || !sameMigrationIndexColumns(index.columns, expected.columns) {
			return errors.New("invalid_processing_shared_derived_blobs_index")
		}
	}
	for _, table := range []struct {
		name      string
		forbidden []string
		required  []string
	}{
		{"backup_asset_processing_uploads", []string{"staging_id"}, []string{"grant_id", "ordinal"}},
		{"backup_asset_derived_artifact_sets", []string{"manifest_digest"}, []string{"job_id"}},
	} {
		ok, err := processingSharedDerivedBlobsUniqueIndexes(db, dbType, table.name, table.forbidden, table.required)
		if err != nil || !ok {
			return errors.New("invalid_processing_shared_derived_blobs_uniqueness")
		}
	}
	guards := []struct{ name, event string }{{processingSharedDerivedBlobsTrigger, "BEFORE INSERT OR UPDATE"}}
	if dbType == "sqlite" {
		guards = []struct{ name, event string }{{processingSharedDerivedBlobsTrigger, "BEFORE INSERT"}, {processingSharedDerivedBlobsUpdateTrigger, "BEFORE UPDATE"}}
	}
	for _, guard := range guards {
		definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", guard.name)
		if err != nil {
			return errors.New("invalid_processing_shared_derived_blobs_admission_trigger")
		}
		if dbType == "sqlite" {
			script, err := processingSharedDerivedBlobsSQLiteUpgrade()
			if err != nil {
				return errors.New("invalid_processing_shared_derived_blobs_admission_trigger")
			}
			start := strings.Index(script, "CREATE TRIGGER "+guard.name+" ")
			if start < 0 {
				return errors.New("invalid_processing_shared_derived_blobs_admission_trigger")
			}
			end := strings.Index(script[start:], "\nEND;")
			if end < 0 || strings.TrimSuffix(normalizeMigrationGuardText(definition), ";") != strings.TrimSuffix(normalizeMigrationGuardText(script[start:start+end+len("\nEND;")]), ";") {
				return errors.New("invalid_processing_shared_derived_blobs_admission_trigger")
			}
			continue
		}
		contract := lifecycleEffectClaimAuditSlotTriggerContract{table: "schema_migrations", name: guard.name, triggerFragments: []string{guard.event}, postgresFunctionName: processingSharedDerivedBlobsFunction, postgresFunctionBody: processingSharedDerivedBlobsPostgresBody}
		enabled, err := migrationTriggerEnabled(db, dbType, "schema_migrations", guard.name)
		if err != nil || !enabled || !lifecyclePostgresGuardDefinitionExact(definition, contract) {
			return errors.New("invalid_processing_shared_derived_blobs_admission_trigger")
		}
		function, err := migrationTriggerFunctionDefinition(db, "schema_migrations", guard.name)
		if err != nil || !lifecyclePostgresGuardFunctionDefinitionExact(function, contract) {
			return errors.New("invalid_processing_shared_derived_blobs_admission_trigger")
		}
	}
	return nil
}

func processingSharedDerivedBlobsUniqueIndexes(db *sql.DB, dbType, table string, forbidden, required []string) (bool, error) {
	var rows *sql.Rows
	var err error
	if dbType == "sqlite" {
		rows, err = db.Query(`SELECT name, partial FROM pragma_index_list(?) WHERE "unique" = 1`, table)
	} else {
		rows, err = db.Query(`SELECT c.relname, (i.indpred IS NOT NULL OR NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive)
			FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
			WHERE i.indrelid = pg_catalog.to_regclass($1) AND i.indisunique`, table)
	}
	if err != nil {
		return false, err
	}
	type candidate struct {
		name    string
		partial bool
	}
	var indexes []candidate
	for rows.Next() {
		var index candidate
		if err := rows.Scan(&index.name, &index.partial); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		return false, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	found := false
	for _, index := range indexes {
		columns, err := migrationIndexColumns(db, dbType, table, index.name)
		if err != nil {
			return false, err
		}
		if sameMigrationIndexColumns(columns, forbidden) {
			return false, nil
		}
		if !index.partial && sameMigrationIndexColumns(columns, required) {
			found = true
		}
	}
	return found, nil
}
