package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// validateNodeProbeGenerationSchema is read-only: a clean version is not enough
// if a restore or manual schema change removed a generation trigger.
func validateNodeProbeGenerationSchema(db *sql.DB, engine string) error {
	for _, item := range []struct {
		table, column, sqliteType, postgresType string
		required                                bool
	}{
		{"nodes", "probe_generation", "text", "text", true},
		{"ssh_keys", "probe_generation", "text", "text", true},
		{"node_probe_state", "node_id", "integer", "bigint", false},
		{"node_probe_state", "admitted_seq", "integer", "bigint", true},
		{"node_probe_state", "applied_seq", "integer", "bigint", true},
		{"node_probe_state", "applied_token", "text", "text", true},
	} {
		column, err := migrationColumnContractOf(db, engine, item.table, item.column)
		typ := item.sqliteType
		if engine == "postgres" {
			typ = item.postgresType
		}
		if err != nil || column.dataType != typ || (item.required && !column.notNull) {
			return fmt.Errorf("invalid_node_probe_column")
		}
		if item.required {
			def := strings.ToLower(strings.TrimSpace(column.defaultSQL))
			if item.column == "admitted_seq" || item.column == "applied_seq" {
				if def != "0" && def != "'0'::bigint" {
					return fmt.Errorf("invalid_node_probe_default")
				}
			} else if def != "''" && def != "''::text" {
				return fmt.Errorf("invalid_node_probe_default")
			}
		}
	}
	pk, err := migrationPrimaryKeyColumns(db, engine, "node_probe_state")
	if err != nil || !sameMigrationIndexColumns(pk, []string{"node_id"}) {
		return fmt.Errorf("invalid_node_probe_primary_key")
	}
	var fk int
	switch engine {
	case "sqlite":
		err = db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list('node_probe_state') WHERE "table" = 'nodes' AND "from" = 'node_id' AND "to" = 'id' AND on_delete = 'CASCADE'`).Scan(&fk)
	case "postgres":
		err = db.QueryRow(`SELECT count(*) FROM pg_catalog.pg_constraint c WHERE c.conrelid = 'node_probe_state'::regclass AND c.confrelid = 'nodes'::regclass AND c.contype = 'f' AND c.confdeltype = 'c' AND c.convalidated AND pg_get_constraintdef(c.oid) = 'FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE'`).Scan(&fk)
	default:
		return fmt.Errorf("invalid_node_probe_engine")
	}
	if err != nil || fk != 1 {
		return fmt.Errorf("invalid_node_probe_foreign_key")
	}

	var source []byte
	if engine == "sqlite" {
		source, err = sqliteMigrationsFS.ReadFile("migrations/sqlite/000100_node_probe_generation.up.sql")
	} else {
		source, err = postgresMigrationsFS.ReadFile("migrations/postgres/000100_node_probe_generation.up.sql")
	}
	if err != nil {
		return fmt.Errorf("invalid_node_probe_source")
	}
	for _, table := range []string{"nodes", "ssh_keys"} {
		if engine == "sqlite" {
			for _, suffix := range []string{"insert", "update"} {
				name := "trg_" + table + "_probe_generation_" + suffix
				start := strings.Index(string(source), "CREATE TRIGGER "+name+" ")
				if start < 0 {
					return fmt.Errorf("invalid_node_probe_source")
				}
				end := strings.Index(string(source[start:]), "\nEND;")
				if end < 0 {
					return fmt.Errorf("invalid_node_probe_source")
				}
				expected := string(source[start : start+end+len("\nEND")])
				actual, err := migrationTriggerDefinition(db, engine, table, name)
				if err != nil || normalizeMigrationGuardBody(actual) != normalizeMigrationGuardBody(expected) {
					return fmt.Errorf("invalid_node_probe_trigger")
				}
			}
		} else {
			name := "trg_" + table + "_probe_generation"
			enabled, err := migrationTriggerEnabled(db, engine, table, name)
			if err != nil || !enabled {
				return fmt.Errorf("invalid_node_probe_trigger")
			}
			var valid bool
			err = db.QueryRow(`SELECT tgtype = 23 AND tgqual IS NULL AND tgattr = ''::int2vector AND p.proname = $3 FROM pg_trigger t JOIN pg_proc p ON p.oid = t.tgfoid WHERE t.tgrelid = to_regclass($1) AND t.tgname = $2 AND NOT t.tgisinternal`, table, name, table+"_probe_generation_set").Scan(&valid)
			if err != nil || !valid {
				return fmt.Errorf("invalid_node_probe_trigger")
			}
			definition, err := migrationTriggerFunctionDefinition(db, table, name)
			body, ok := migrationPostgresFunctionBody(definition)
			start := strings.Index(string(source), "CREATE FUNCTION "+table+"_probe_generation_set()")
			if start < 0 {
				return fmt.Errorf("invalid_node_probe_source")
			}
			expected, found := migrationPostgresFunctionBody(string(source[start:]))
			// Restrict the parser to this function; later functions also use $$.
			if split := strings.Index(string(source[start:]), "\n$$;"); split >= 0 {
				expected, found = migrationPostgresFunctionBody(string(source[start : start+split+len("\n$$;")]))
			}
			if err != nil || !ok || !found || normalizeMigrationGuardBody(body) != normalizeMigrationGuardBody(expected) {
				return fmt.Errorf("invalid_node_probe_function")
			}
		}
	}
	return nil
}
