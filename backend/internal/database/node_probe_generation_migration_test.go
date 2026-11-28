package database

import (
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"
)

func TestNodeProbeGenerationMigrationSQLite(t *testing.T) {
	testNodeProbeGenerationMigration(t, newSQLiteMigrationFixture(t))
}

func TestNodeProbeGenerationMigrationPostgres(t *testing.T) {
	testNodeProbeGenerationMigration(t, newRequiredPostgresMigrationFixture(t))
}

var probeGenerationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func testNodeProbeGenerationMigration(t *testing.T, f migrationFixture) {
	t.Helper()
	t.Run("UpgradeAndReupgrade", func(t *testing.T) {
		m, db := f.openAt(t, 99)
		f.mustExec(t, db, `INSERT INTO ssh_keys (id, name, username, private_key, fingerprint) VALUES (101, 'key101', 'root', 'key', 'fp')`)
		f.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, backup_dir, ssh_key_id) VALUES (101, 'node101', 'host', 'root', 'node101', 101)`)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 100)
		if err := validateMinimumRecoverySchema(db, f.engine, 100); err != nil {
			t.Fatal(err)
		}
		node := probeTestGeneration(t, db, "nodes", 101)
		key := probeTestGeneration(t, db, "ssh_keys", 101)
		if node == key {
			t.Fatal("existing rows received the same generation")
		}
		f.mustExec(t, db, `INSERT INTO node_probe_state (node_id, admitted_seq, applied_seq, applied_token) VALUES (101, 2, 2, 'token')`)
		if err := m.Steps(-1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 99)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = 101 AND host = 'host'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("node not retained: %d %v", count, err)
		}
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 100)
		if got := probeTestGeneration(t, db, "nodes", 101); got == node {
			t.Fatal("node generation survived reupgrade")
		}
		if got := probeTestGeneration(t, db, "ssh_keys", 101); got == key {
			t.Fatal("key generation survived reupgrade")
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM node_probe_state`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("ordering state survived reupgrade: %d %v", count, err)
		}
		if err := validateMinimumRecoverySchema(db, f.engine, 100); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("GenerationChanges", func(t *testing.T) {
		_, db := f.openAt(t, 100)
		f.mustExec(t, db, `INSERT INTO ssh_keys (id, name, username, private_key, fingerprint) VALUES (201, 'key201', 'root', 'key', 'fp')`)
		f.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (201, 'node201', 'host', 'root', 'node201')`)
		for _, test := range []struct {
			table, column string
			value         any
		}{
			{"nodes", "host", "new-host"}, {"nodes", "port", 2222}, {"nodes", "username", "other"},
			{"nodes", "auth_type", "password"}, {"nodes", "ssh_key_id", 201}, {"nodes", "password", "secret"},
			{"nodes", "private_key", "private"}, {"nodes", "tags", "tag"}, {"nodes", "archived", true},
			{"ssh_keys", "private_key", "new-key"}, {"ssh_keys", "disabled", true},
			{"ssh_keys", "expires_at", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, {"ssh_keys", "allowed_purposes", "backup"},
			{"ssh_keys", "allowed_node_ids", "201"}, {"ssh_keys", "allowed_node_tags", "tag"},
		} {
			t.Run(test.table+"/"+test.column, func(t *testing.T) {
				before := probeTestGeneration(t, db, test.table, 201)
				f.mustExec(t, db, "UPDATE "+test.table+" SET "+test.column+" = ? WHERE id = 201", test.value)
				after := probeTestGeneration(t, db, test.table, 201)
				if before == after {
					t.Fatal("relevant update did not change generation")
				}
				f.mustExec(t, db, "UPDATE "+test.table+" SET "+test.column+" = ? WHERE id = 201", test.value)
				if got := probeTestGeneration(t, db, test.table, 201); got != after {
					t.Fatal("unchanged value changed generation")
				}
			})
		}
		before := probeTestGeneration(t, db, "nodes", 201)
		f.mustExec(t, db, `UPDATE nodes SET status = 'online' WHERE id = 201`)
		if got := probeTestGeneration(t, db, "nodes", 201); got != before {
			t.Fatal("health update changed identity")
		}
		beforeNull := probeTestGeneration(t, db, "nodes", 201)
		f.mustExec(t, db, `UPDATE nodes SET ssh_key_id = NULL WHERE id = 201`)
		if got := probeTestGeneration(t, db, "nodes", 201); got == beforeNull {
			t.Fatal("transition to NULL did not change generation")
		}
		beforeNull = probeTestGeneration(t, db, "ssh_keys", 201)
		f.mustExec(t, db, `UPDATE ssh_keys SET expires_at = NULL WHERE id = 201`)
		if got := probeTestGeneration(t, db, "ssh_keys", 201); got == beforeNull {
			t.Fatal("key transition to NULL did not change generation")
		}
		f.mustExec(t, db, `INSERT INTO node_probe_state (node_id) VALUES (201)`)
		f.mustExec(t, db, `DELETE FROM nodes WHERE id = 201`)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM node_probe_state WHERE node_id = 201`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("sidecar was not cascaded: %d %v", count, err)
		}
	})
	t.Run("TriggerDrift", func(t *testing.T) {
		m, db := f.openAt(t, 100)
		if f.engine == "sqlite" {
			f.mustExec(t, db, `DROP TRIGGER trg_nodes_probe_generation_update`)
			f.mustExec(t, db, `CREATE TRIGGER trg_nodes_probe_generation_update AFTER UPDATE ON nodes BEGIN SELECT 1; END`)
		} else {
			f.mustExec(t, db, `CREATE OR REPLACE FUNCTION nodes_probe_generation_set() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		}
		if err := validateMinimumRecoverySchema(db, f.engine, 100); !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("expected trigger drift, got %v", err)
		}
		assertMigrationVersion(t, m, 100)
	})
}

func probeTestGeneration(t *testing.T, db *sql.DB, table string, id int) string {
	t.Helper()
	var value string
	// IDs are fixed test constants and table comes from a fixed list above.
	query := "SELECT probe_generation FROM " + table + " WHERE id = 201"
	if id == 101 {
		query = "SELECT probe_generation FROM " + table + " WHERE id = 101"
	}
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if !probeGenerationPattern.MatchString(value) {
		t.Fatalf("invalid generation %q", value)
	}
	return value
}
