package database

import (
	"database/sql"
	"errors"
	"strings"
)

const exportRawFingerprintSchemaVersion int64 = 97

const (
	exportRawFingerprintAdmissionTrigger       = "trg_export_raw_fingerprint_downgrade_admission"
	exportRawFingerprintUpdateAdmissionTrigger = "trg_export_raw_fingerprint_downgrade_update_admission"
	exportRawFingerprintAdmissionFunction      = "export_raw_fingerprint_downgrade_admission"
	exportRawFingerprintWhen                   = `NEW.version < 97 AND (
    EXISTS (SELECT 1 FROM backup_asset_export_items WHERE entry_fingerprint = '')
    OR EXISTS (SELECT 1 FROM sqlite_master WHERE tbl_name IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND NOT
            (tbl_name = 'backup_asset_export_items' AND name = 'idx_backup_asset_export_items_job_state' AND
             lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) =
             'createindexidx_backup_asset_export_items_job_stateonbackup_asset_export_items(job_id,state,ordinal)'))))
    OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND NOT (source.name = 'backup_asset_export_item_attempts' AND ref."table" = 'backup_asset_export_items'
            AND ref."from" = 'item_id' AND ref."to" = 'id'))
)`
	exportRawFingerprintSQLiteBody   = `SELECT RAISE(ABORT, '000097 downgrade blocked: export fingerprint or extra schema objects exist');`
	exportRawFingerprintPostgresBody = `BEGIN
    IF NEW.version < 97 AND EXISTS (SELECT 1 FROM backup_asset_export_items WHERE entry_fingerprint = '') THEN
        RAISE EXCEPTION '000097 downgrade blocked: empty export entry fingerprint exists';
    END IF;
    RETURN NEW;
END;`
)

func validateExportRawFingerprintSchema(db *sql.DB, dbType string) error {
	for _, name := range []string{"entry_fingerprint", "fingerprint_strength", "entry_type"} {
		column, err := migrationColumnContractOf(db, dbType, "backup_asset_export_items", name)
		if err != nil || !column.notNull {
			return errors.New("invalid_export_raw_fingerprint_column")
		}
	}
	checks, err := migrationTableCheckDefinitions(db, dbType, "backup_asset_export_items")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	typeCheck := `entry_type IN ('file', 'directory', 'symlink', 'hardlink', 'special', 'unknown')`
	strengthCheck := `fingerprint_strength IN ('strong', 'weak', 'none')`
	fingerprintCheck := `length(entry_fingerprint) <= 128 AND (length(entry_fingerprint) > 0 OR (fingerprint_strength = 'none' AND entry_type IN ('directory', 'symlink', 'hardlink', 'special', 'unknown')))`
	if dbType == "postgres" {
		typeCheck = `entry_type = ANY (ARRAY['file', 'directory', 'symlink', 'hardlink', 'special', 'unknown'])`
		strengthCheck = `fingerprint_strength = ANY (ARRAY['strong', 'weak', 'none'])`
		fingerprintCheck = `length(entry_fingerprint) <= 128 AND (length(entry_fingerprint) > 0 OR (fingerprint_strength = 'none' AND entry_type = ANY (ARRAY['directory', 'symlink', 'hardlink', 'special', 'unknown'])))`
	}
	fingerprints, types, strengths := 0, 0, 0
	for _, check := range checks {
		if strings.Contains(strings.ToLower(check.expression), "entry_fingerprint") {
			fingerprints++
			if !check.validated || exportFingerprintBooleanExpression(check.expression) != exportFingerprintBooleanExpression(fingerprintCheck) {
				return errors.New("invalid_export_raw_fingerprint_check")
			}
		}
		if exportFingerprintBooleanExpression(check.expression) == exportFingerprintBooleanExpression(typeCheck) && check.validated {
			types++
		}
		if exportFingerprintBooleanExpression(check.expression) == exportFingerprintBooleanExpression(strengthCheck) && check.validated {
			strengths++
		}
	}
	if fingerprints != 1 || types != 1 || strengths != 1 {
		return errors.New("invalid_export_raw_fingerprint_check")
	}
	guards := []struct{ name, event string }{{exportRawFingerprintAdmissionTrigger, "BEFORE INSERT OR UPDATE"}}
	if dbType == "sqlite" {
		guards = []struct{ name, event string }{
			{exportRawFingerprintAdmissionTrigger, "BEFORE INSERT"},
			{exportRawFingerprintUpdateAdmissionTrigger, "BEFORE UPDATE"},
		}
	}
	for _, guard := range guards {
		definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", guard.name)
		if err != nil {
			return errors.New("missing_export_raw_fingerprint_admission_trigger")
		}
		contract := lifecycleEffectClaimAuditSlotTriggerContract{
			table: "schema_migrations", name: guard.name, triggerFragments: []string{guard.event},
			sqliteWhen: exportRawFingerprintWhen, sqliteBody: exportRawFingerprintSQLiteBody,
			postgresFunctionName: exportRawFingerprintAdmissionFunction, postgresFunctionBody: exportRawFingerprintPostgresBody,
		}
		if dbType == "sqlite" {
			if !lifecycleSQLiteGuardDefinitionExact(definition, contract) {
				return errors.New("invalid_export_raw_fingerprint_admission_trigger")
			}
			continue
		}
		enabled, err := migrationTriggerEnabled(db, dbType, "schema_migrations", guard.name)
		if err != nil || !enabled || !lifecyclePostgresGuardDefinitionExact(definition, contract) {
			return errors.New("invalid_export_raw_fingerprint_admission_trigger")
		}
		function, err := migrationTriggerFunctionDefinition(db, "schema_migrations", guard.name)
		if err != nil || !lifecyclePostgresGuardFunctionDefinitionExact(function, contract) {
			return errors.New("invalid_export_raw_fingerprint_admission_trigger")
		}
	}
	return nil
}

func preflightExportRawFingerprintMigration(db *sql.DB, dbType string, version int64) error {
	if dbType != "sqlite" || version >= exportRawFingerprintSchemaVersion {
		return nil
	}
	var extra bool
	err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM sqlite_master
		WHERE tbl_name IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
		AND (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND NOT
			(tbl_name = 'backup_asset_export_items' AND name = 'idx_backup_asset_export_items_job_state' AND
			 lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) =
			 'createindexidx_backup_asset_export_items_job_stateonbackup_asset_export_items(job_id,state,ordinal)'))))
		OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
			WHERE source.type = 'table' AND ref."table" IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
			AND NOT (source.name = 'backup_asset_export_item_attempts' AND ref."table" = 'backup_asset_export_items'
				AND ref."from" = 'item_id' AND ref."to" = 'id'))`).Scan(&extra)
	if err != nil {
		return migrationSchemaDriftError(version, "export_raw_fingerprint_schema_query_failed")
	}
	if extra {
		return migrationSchemaDriftError(version, "unexpected_export_raw_fingerprint_sqlite_object")
	}
	return nil
}

// Compare the AND/OR tree, preserving grouping while accepting PostgreSQL's
// redundant parentheses and type casts in catalog deparsing.
func exportFingerprintBooleanExpression(expression string) string {
	expression = strings.TrimSpace(expression)
	for strings.HasPrefix(expression, "(") {
		end, ok := migrationMatchingParenthesis(expression, 0)
		if !ok || end != len(expression)-1 {
			break
		}
		expression = strings.TrimSpace(expression[1:end])
	}
	for _, operator := range []string{"OR", "AND"} {
		depth := 0
		quoted := false
		for i := 0; i < len(expression); i++ {
			if expression[i] == '\'' {
				if quoted && i+1 < len(expression) && expression[i+1] == '\'' {
					i++
					continue
				}
				quoted = !quoted
				continue
			}
			if quoted {
				continue
			}
			switch expression[i] {
			case '(':
				depth++
			case ')':
				depth--
			default:
				end := i + len(operator)
				if depth == 0 && end <= len(expression) && strings.EqualFold(expression[i:end], operator) &&
					(i == 0 || !migrationSQLWordCharacter(expression[i-1])) &&
					(end == len(expression) || !migrationSQLWordCharacter(expression[end])) {
					return operator + "(" + exportFingerprintBooleanExpression(expression[:i]) + "," + exportFingerprintBooleanExpression(expression[end:]) + ")"
				}
			}
		}
	}
	return normalizeMigrationPredicate(expression)
}
