package database

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

const accountRecoverySchemaVersion int64 = 94

const (
	accountRecoveryAdmissionTrigger       = "trg_account_recovery_downgrade_admission"
	accountRecoveryUpdateAdmissionTrigger = "trg_account_recovery_downgrade_update_admission"
	accountRecoveryAdmissionFunction      = "account_recovery_downgrade_admission"
	accountRecoverySQLiteWhen             = `NEW.version < 94 AND (
    EXISTS (SELECT 1 FROM account_recovery_audits)
    OR EXISTS (SELECT 1 FROM users WHERE totp_reenrollment_required = TRUE)
)`
	accountRecoverySQLiteBody   = `SELECT RAISE(ABORT, '000094 downgrade blocked: account recovery state exists');`
	accountRecoveryPostgresBody = `BEGIN
    IF NEW.version < 94 AND (
        EXISTS (SELECT 1 FROM account_recovery_audits)
        OR EXISTS (SELECT 1 FROM users WHERE totp_reenrollment_required = TRUE)
    ) THEN
        RAISE EXCEPTION '000094 downgrade blocked: account recovery state exists';
    END IF;
    RETURN NEW;
END;`
)

// VerifyCurrentSchema is a read-only admission check for offline tools. It
// requires exactly one clean metadata row at this binary's embedded migration
// head and validates the live schema contracts, including recovery guards. It
// never initializes a migrator, creates tables, repairs drift or advances schema.
// Callers must open an existing database and keep all application writers stopped.
func VerifyCurrentSchema(db *gorm.DB, dbType string) error {
	sqlDB, err := db.DB()
	if err != nil {
		return migrationSchemaDriftError(0, "database_unavailable")
	}
	dirty, version, err := checkMigrationDirty(sqlDB, dbType)
	if err != nil {
		return migrationSchemaDriftError(0, "metadata_query_failed")
	}
	if dirty {
		return ErrMigrationDirty
	}
	head, err := currentCompiledSchemaVersion(dbType)
	if err != nil {
		return migrationSchemaDriftError(version, "compiled_schema_unavailable")
	}
	if version != head {
		return migrationSchemaDriftError(version, "not_current_compiled_version")
	}
	var rows int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&rows); err != nil {
		return migrationSchemaDriftError(version, "metadata_query_failed")
	}
	if rows != 1 {
		return migrationSchemaDriftError(version, "invalid_migration_metadata")
	}
	return validateMinimumRecoverySchema(sqlDB, dbType, version)
}

func currentCompiledSchemaVersion(dbType string) (int64, error) {
	fs, directory := sqliteMigrationsFS, "migrations/sqlite"
	switch dbType {
	case "sqlite":
	case "postgres":
		fs, directory = postgresMigrationsFS, "migrations/postgres"
	default:
		return 0, errors.New("unsupported_database")
	}
	entries, err := fs.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	var head int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return 0, errors.New("invalid_compiled_migration")
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, err
		}
		if version > head {
			head = version
		}
	}
	if head == 0 {
		return 0, errors.New("missing_compiled_migration")
	}
	return head, nil
}

func validateIdentitySecurityColumns(db *sql.DB, dbType string, version int64) error {
	// Check the persisted identity projection before upgrades or offline writes.
	tables := map[string][]string{
		"users":               {"id", "username", "password_hash", "role", "totp_secret", "totp_enabled", "recovery_codes", "token_version", "totp_enrollment_id", "totp_enrollment_expires_at"},
		"auth_pending_tokens": {"jti", "user_id", "token_version", "totp_binding", "expires_at", "consumed_at", "created_at"},
		"break_glass_audits":  {"id", "target_user_id", "target_username", "reason", "created_at"},
	}
	if version >= accountRecoverySchemaVersion {
		tables["users"] = append(tables["users"], "totp_reenrollment_required")
		tables["account_recovery_audits"] = []string{"id", "operation_id", "target_user_id", "target_username", "event", "reset_password", "reset_totp", "reason", "old_token_version", "new_token_version", "revoked_grant_count", "created_at"}
	}
	for table, columns := range tables {
		exists, err := migrationRelationExists(db, dbType, table, "table")
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !exists {
			return errors.New("missing_account_recovery_table")
		}
		for _, column := range columns {
			exists, err := migrationColumnExists(db, dbType, table, column)
			if err != nil {
				return errors.New("catalog_query_failed")
			}
			if !exists {
				return errors.New("missing_account_recovery_column")
			}
		}
	}
	return nil
}

func validateAccountRecoverySchema(db *sql.DB, dbType string) error {
	if err := validateAccountRecoveryColumns(db, dbType); err != nil {
		return err
	}
	pk, err := migrationPrimaryKeyColumns(db, dbType, "account_recovery_audits")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if !sameMigrationIndexColumns(pk, []string{"id"}) {
		return errors.New("invalid_account_recovery_primary_key")
	}
	foreignKeys, err := migrationForeignKeyCount(db, dbType, "account_recovery_audits")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if foreignKeys != 0 {
		return errors.New("invalid_account_recovery_foreign_key")
	}
	for _, want := range []struct {
		name   string
		column string
		unique bool
	}{
		{"idx_account_recovery_audits_operation_id", "operation_id", true},
		{"idx_account_recovery_audits_target_user_id", "target_user_id", false},
		{"idx_account_recovery_audits_created_at", "created_at", false},
	} {
		got, err := migrationIndexContractOf(db, dbType, "account_recovery_audits", want.name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("missing_account_recovery_index")
			}
			return errors.New("catalog_query_failed")
		}
		if !migrationIndexUsable(got) || got.unique != want.unique ||
			!sameMigrationIndexColumns(got.columns, []string{want.column}) || strings.TrimSpace(got.predicate) != "" {
			return errors.New("invalid_account_recovery_index")
		}
	}
	if err := validateAccountRecoveryChecks(db, dbType); err != nil {
		return err
	}
	return validateAccountRecoveryAdmission(db, dbType)
}

func validateAccountRecoveryColumns(db *sql.DB, dbType string) error {
	flag, err := migrationColumnContractOf(db, dbType, "users", "totp_reenrollment_required")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if flag.dataType != "boolean" || !flag.notNull || normalizeMigrationSQLToken(flag.defaultSQL) != "false" {
		return errors.New("invalid_account_recovery_required_column")
	}
	count, err := migrationColumnCount(db, dbType, "account_recovery_audits")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if count != 12 {
		return errors.New("invalid_account_recovery_columns")
	}
	for _, want := range []struct {
		name         string
		sqliteType   string
		postgresType string
		maxLength    int64
	}{
		{"id", "integer", "bigint", 0},
		{"operation_id", "varchar(32)", "character varying", 32},
		{"target_user_id", "integer", "bigint", 0},
		{"target_username", "varchar(64)", "character varying", 64},
		{"event", "varchar(32)", "character varying", 32},
		{"reset_password", "boolean", "boolean", 0},
		{"reset_totp", "boolean", "boolean", 0},
		{"reason", "text", "text", 0},
		{"old_token_version", "integer", "bigint", 0},
		{"new_token_version", "integer", "bigint", 0},
		{"revoked_grant_count", "integer", "bigint", 0},
		{"created_at", "datetime", "timestamp with time zone", 0},
	} {
		got, err := migrationColumnContractOf(db, dbType, "account_recovery_audits", want.name)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		wantType, wantNotNull := want.sqliteType, want.name != "id"
		if dbType == "postgres" {
			wantType, wantNotNull = want.postgresType, true
			if want.maxLength != got.maxLength {
				return errors.New("invalid_account_recovery_column")
			}
		}
		if got.dataType != wantType || got.notNull != wantNotNull {
			return errors.New("invalid_account_recovery_column")
		}
		if want.name == "id" && dbType == "postgres" {
			if !strings.HasPrefix(got.defaultSQL, "nextval(") {
				return errors.New("invalid_account_recovery_id_default")
			}
		} else if got.defaultSQL != "" {
			return errors.New("invalid_account_recovery_column_default")
		}
	}
	return nil
}

func validateAccountRecoveryChecks(db *sql.DB, dbType string) error {
	checks := []string{
		"length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'",
		"target_user_id > 0",
		"event IN ('reset', 'reenrollment_completed')",
		"reset_password IN (0, 1)",
		"reset_totp IN (0, 1)",
		"old_token_version >= 0",
		"new_token_version = old_token_version + 1",
		"revoked_grant_count >= 0",
		"(event = 'reset' AND (reset_password OR reset_totp) AND length(CAST(reason AS BLOB)) BETWEEN 8 AND 512) OR (event = 'reenrollment_completed' AND reason = '')",
	}
	if dbType == "postgres" {
		checks = []string{
			"operation_id ~ '^[0-9a-f]{32}$'",
			"target_user_id > 0",
			"event = ANY (ARRAY['reset', 'reenrollment_completed'])",
			"old_token_version >= 0",
			"new_token_version = old_token_version + 1",
			"revoked_grant_count >= 0",
			"(event = 'reset' AND (reset_password OR reset_totp) AND octet_length(reason) >= 8 AND octet_length(reason) <= 512) OR (event = 'reenrollment_completed' AND reason = '')",
		}
	}
	got, err := migrationTableCheckDefinitions(db, dbType, "account_recovery_audits")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if !migrationCheckMultisetExact(dbType, got, checks) {
		return errors.New("invalid_account_recovery_check")
	}
	if dbType == "sqlite" {
		userChecks, err := migrationTableCheckDefinitions(db, dbType, "users")
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		found := false
		for _, check := range userChecks {
			if migrationCheckMultisetExact(dbType, []migrationCheckDefinition{check}, []string{"totp_reenrollment_required IN (0, 1)"}) {
				found = true
			}
		}
		if !found {
			return errors.New("invalid_account_recovery_required_check")
		}
	}
	return nil
}

func validateAccountRecoveryAdmission(db *sql.DB, dbType string) error {
	guards := []struct{ name, event string }{{accountRecoveryAdmissionTrigger, "BEFORE INSERT OR UPDATE"}}
	if dbType == "sqlite" {
		guards = []struct{ name, event string }{
			{accountRecoveryAdmissionTrigger, "BEFORE INSERT"},
			{accountRecoveryUpdateAdmissionTrigger, "BEFORE UPDATE"},
		}
	}
	for _, guard := range guards {
		definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", guard.name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("missing_account_recovery_admission_trigger")
			}
			return errors.New("catalog_query_failed")
		}
		contract := lifecycleEffectClaimAuditSlotTriggerContract{
			table: "schema_migrations", name: guard.name, triggerFragments: []string{guard.event},
			sqliteWhen: accountRecoverySQLiteWhen, sqliteBody: accountRecoverySQLiteBody,
			postgresFunctionName: accountRecoveryAdmissionFunction, postgresFunctionBody: accountRecoveryPostgresBody,
		}
		if dbType == "sqlite" {
			if !lifecycleSQLiteGuardDefinitionExact(definition, contract) {
				return errors.New("invalid_account_recovery_admission_trigger")
			}
			continue
		}
		enabled, err := migrationTriggerEnabled(db, dbType, "schema_migrations", guard.name)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !enabled || !lifecyclePostgresGuardDefinitionExact(definition, contract) {
			return errors.New("invalid_account_recovery_admission_trigger")
		}
		function, err := migrationTriggerFunctionDefinition(db, "schema_migrations", guard.name)
		if err != nil {
			return errors.New("catalog_query_failed")
		}
		if !lifecyclePostgresGuardFunctionDefinitionExact(function, contract) {
			return errors.New("invalid_account_recovery_admission_trigger")
		}
	}
	return nil
}
