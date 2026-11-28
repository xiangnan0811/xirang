package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// validateNodeConfigRevisionSchema checks the actual revision writers, not only
// their names, before admitting application writes or offline recovery.
func validateNodeConfigRevisionSchema(db *sql.DB, engine string) error {
	for _, item := range []struct{ table, column string }{
		{"nodes", "config_revision"},
		{"node_probe_state", "applied_status"},
	} {
		column, err := migrationColumnContractOf(db, engine, item.table, item.column)
		def := strings.ToLower(strings.TrimSpace(column.defaultSQL))
		if err != nil || column.dataType != "text" || !column.notNull || (def != "''" && def != "''::text") {
			return fmt.Errorf("invalid_node_config_revision_column")
		}
	}

	var source []byte
	var err error
	switch engine {
	case "sqlite":
		source, err = sqliteMigrationsFS.ReadFile("migrations/sqlite/000101_node_config_revision.up.sql")
	case "postgres":
		source, err = postgresMigrationsFS.ReadFile("migrations/postgres/000101_node_config_revision.up.sql")
	default:
		return fmt.Errorf("invalid_node_config_revision_engine")
	}
	if err != nil {
		return fmt.Errorf("invalid_node_config_revision_source")
	}
	text := string(source)
	if engine == "sqlite" {
		for _, suffix := range []string{"insert", "update"} {
			name := "trg_nodes_config_revision_" + suffix
			start := strings.Index(text, "CREATE TRIGGER "+name+" ")
			if start < 0 {
				return fmt.Errorf("invalid_node_config_revision_source")
			}
			end := strings.Index(text[start:], "\nEND;")
			if end < 0 {
				return fmt.Errorf("invalid_node_config_revision_source")
			}
			expected := text[start : start+end+len("\nEND")]
			actual, err := migrationTriggerDefinition(db, engine, "nodes", name)
			if err != nil || normalizeMigrationGuardBody(actual) != normalizeMigrationGuardBody(expected) {
				return fmt.Errorf("invalid_node_config_revision_trigger")
			}
		}
		return nil
	}

	const name = "trg_nodes_config_revision"
	enabled, err := migrationTriggerEnabled(db, engine, "nodes", name)
	if err != nil || !enabled {
		return fmt.Errorf("invalid_node_config_revision_trigger")
	}
	var valid bool
	err = db.QueryRow(`SELECT t.tgtype = 23 AND t.tgqual IS NULL AND t.tgattr = ''::int2vector
		AND p.proname = 'nodes_config_revision_set' AND p.pronargs = 0
		AND p.prorettype = 'trigger'::regtype AND NOT p.prosecdef AND p.proconfig IS NULL
		AND l.lanname = 'plpgsql'
		FROM pg_trigger t JOIN pg_proc p ON p.oid = t.tgfoid
		JOIN pg_language l ON l.oid = p.prolang
		WHERE t.tgrelid = to_regclass('nodes') AND t.tgname = $1 AND NOT t.tgisinternal`, name).Scan(&valid)
	if err != nil || !valid {
		return fmt.Errorf("invalid_node_config_revision_trigger")
	}
	definition, err := migrationTriggerFunctionDefinition(db, "nodes", name)
	body, ok := migrationPostgresFunctionBody(definition)
	start := strings.Index(text, "CREATE FUNCTION nodes_config_revision_set()")
	if start < 0 {
		return fmt.Errorf("invalid_node_config_revision_source")
	}
	end := strings.Index(text[start:], "\n$$;")
	if end < 0 {
		return fmt.Errorf("invalid_node_config_revision_source")
	}
	expected, found := migrationPostgresFunctionBody(text[start : start+end+len("\n$$;")])
	if err != nil || !ok || !found || normalizeMigrationGuardBody(body) != normalizeMigrationGuardBody(expected) {
		return fmt.Errorf("invalid_node_config_revision_function")
	}
	return nil
}
