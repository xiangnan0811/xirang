package database

import (
	"bytes"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/nodeprobe"
)

func TestNodeConfigRevisionMigrationSQLite(t *testing.T) {
	testNodeConfigRevisionMigration(t, newSQLiteMigrationFixture(t))
}

func TestNodeConfigRevisionMigrationPostgres(t *testing.T) {
	testNodeConfigRevisionMigration(t, newRequiredPostgresMigrationFixture(t))
}

// Version 100 token for the probe of node 101 at sequence 6 with this generation
// and no bound key. This fixed vector also records the deployed framing.
const revisionMigrationProbeGeneration = "00112233445566778899aabbccddeeff"
const revisionMigrationProbeToken = "f6bb47ca0f6151b9025145f5437db9d328bb6db8d1eac25caa37a26d27326234"

func testNodeConfigRevisionMigration(t *testing.T, f migrationFixture) {
	t.Helper()
	t.Run("UpgradeDowngradeReupgrade", func(t *testing.T) {
		m, db := f.openAt(t, 100)
		f.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, backup_dir, password, private_key) VALUES (101, 'node101', 'host', 'root', 'node101', 'enc:v2:known-password-bytes', 'enc:v1:known-key-bytes')`)
		f.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (102, 'node102', 'host', 'root', 'node102')`)
		f.mustExec(t, db, `UPDATE nodes SET probe_generation = ? WHERE id = 101`, revisionMigrationProbeGeneration)
		f.mustExec(t, db, `INSERT INTO node_probe_state (node_id, admitted_seq, applied_seq, applied_token) VALUES (101, 6, 6, ?)`, revisionMigrationProbeToken)
		assertNodeMigrationSecretBytes(t, db)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 101)
		if err := VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine); err != nil {
			t.Fatal(err)
		}
		first := nodeConfigRevision(t, db, 101)
		if second := nodeConfigRevision(t, db, 102); second == first {
			t.Fatal("existing nodes received the same revision")
		}
		assertNodeProbeHistory(t, db, "", true)
		node := model.Node{ID: 101, ProbeGeneration: probeTestGeneration(t, db, "nodes", 101)}
		if got := nodeprobe.IdentityToken(node, 6, time.Time{}); got != revisionMigrationProbeToken {
			t.Fatalf("v100 probe no longer matches after upgrade: %s", got)
		}
		assertNodeMigrationSecretBytes(t, db)
		f.mustExec(t, db, `UPDATE node_probe_state SET applied_status = 'online' WHERE node_id = 101`)
		if err := m.Steps(-1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 100)
		if err := validateMinimumRecoverySchema(db, f.engine, 100); err != nil {
			t.Fatal(err)
		}
		assertNodeProbeHistory(t, db, "", false)
		assertNodeMigrationSecretBytes(t, db)
		if got := probeTestGeneration(t, db, "nodes", 101); got != revisionMigrationProbeGeneration {
			t.Fatal("downgrade changed probe identity")
		}
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 101)
		if got := nodeConfigRevision(t, db, 101); got == first {
			t.Fatal("reupgrade reused a revision")
		}
		assertNodeProbeHistory(t, db, "", true)
		assertNodeMigrationSecretBytes(t, db)
		if got := probeTestGeneration(t, db, "nodes", 101); got != revisionMigrationProbeGeneration {
			t.Fatal("reupgrade changed probe identity")
		}
		if err := VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ConfigAndActivityWriters", func(t *testing.T) {
		_, db := f.openAt(t, 101)
		f.mustExec(t, db, `INSERT INTO ssh_keys (id, name, username, private_key, fingerprint) VALUES (201, 'key201', 'root', 'key', 'fp')`)
		f.mustExec(t, db, `INSERT INTO escalation_policies (id, name, min_severity, levels) VALUES (201, 'policy201', 'warning', '[]')`)
		f.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, backup_dir, config_revision) VALUES (201, 'node201', 'host', 'root', 'node201', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`)
		if got := nodeConfigRevision(t, db, 201); got == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatal("insert retained a caller-supplied revision")
		}
		instant := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, test := range []struct {
			column string
			value  any
		}{
			{"name", "renamed"}, {"host", "new-host"}, {"port", 2222}, {"username", "other"},
			{"auth_type", "password"}, {"ssh_key_id", 201}, {"password", "secret"}, {"private_key", "private"},
			{"tags", "tag"}, {"base_path", "/base"}, {"backup_dir", "renamed-backup"}, {"use_sudo", true},
			{"maintenance_start", instant}, {"maintenance_end", instant}, {"expiry_date", instant},
			{"archived", true}, {"escalation_policy_id", 201},
		} {
			t.Run(test.column, func(t *testing.T) {
				before := nodeConfigRevision(t, db, 201)
				f.mustExec(t, db, "UPDATE nodes SET "+test.column+" = ? WHERE id = 201", test.value)
				after := nodeConfigRevision(t, db, 201)
				if before == after {
					t.Fatal("configuration change did not change revision")
				}
				f.mustExec(t, db, "UPDATE nodes SET "+test.column+" = ? WHERE id = 201", test.value)
				if got := nodeConfigRevision(t, db, 201); got != after {
					t.Fatal("unchanged configuration changed revision")
				}
			})
		}
		for _, column := range []string{"ssh_key_id", "maintenance_start", "maintenance_end", "expiry_date", "escalation_policy_id"} {
			before := nodeConfigRevision(t, db, 201)
			f.mustExec(t, db, "UPDATE nodes SET "+column+" = NULL WHERE id = 201")
			if got := nodeConfigRevision(t, db, 201); got == before {
				t.Fatalf("%s transition to NULL did not change revision", column)
			}
		}
		for _, test := range []struct {
			column string
			value  any
		}{
			{"status", "online"}, {"connection_latency", 45}, {"last_seen_at", instant},
			{"last_backup_at", instant}, {"updated_at", instant},
		} {
			t.Run("activity/"+test.column, func(t *testing.T) {
				before := nodeConfigRevision(t, db, 201)
				f.mustExec(t, db, "UPDATE nodes SET "+test.column+" = ? WHERE id = 201", test.value)
				if got := nodeConfigRevision(t, db, 201); got != before {
					t.Fatal("activity changed revision")
				}
			})
		}
	})
	t.Run("TriggerDrift", func(t *testing.T) {
		for _, kind := range []string{"insert", "update"} {
			t.Run(kind, func(t *testing.T) {
				m, db := f.openAt(t, 101)
				if f.engine == "sqlite" {
					name := "trg_nodes_config_revision_" + kind
					f.mustExec(t, db, "DROP TRIGGER "+name)
					f.mustExec(t, db, "CREATE TRIGGER "+name+" AFTER "+kind+" ON nodes BEGIN SELECT 1; END")
				} else if kind == "insert" {
					f.mustExec(t, db, `ALTER TABLE nodes DISABLE TRIGGER trg_nodes_config_revision`)
				} else {
					f.mustExec(t, db, `CREATE OR REPLACE FUNCTION nodes_config_revision_set() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
				}
				for _, verify := range []func() error{
					func() error { return VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine) },
					func() error { return RunMigrations(f.recoveryWorkerGorm(t, db), f.engine) },
				} {
					if err := verify(); !errors.Is(err, ErrMigrationSchemaDrift) {
						t.Fatalf("expected schema drift, got %v", err)
					}
				}
				assertMigrationVersion(t, m, 101)
			})
		}
	})
	t.Run("ColumnDrift", func(t *testing.T) {
		m, db := f.openAt(t, 101)
		f.mustExec(t, db, `ALTER TABLE node_probe_state DROP COLUMN applied_status`)
		for _, verify := range []func() error{
			func() error { return VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine) },
			func() error { return RunMigrations(f.recoveryWorkerGorm(t, db), f.engine) },
		} {
			if err := verify(); !errors.Is(err, ErrMigrationSchemaDrift) {
				t.Fatalf("expected column drift, got %v", err)
			}
		}
		assertMigrationVersion(t, m, 101)
	})
}

func nodeConfigRevision(t *testing.T, db *sql.DB, id int) string {
	t.Helper()
	var value string
	if err := db.QueryRow(`SELECT config_revision FROM nodes WHERE id = ` + strconv.Itoa(id)).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if !probeGenerationPattern.MatchString(value) {
		t.Fatalf("invalid revision %q", value)
	}
	return value
}

func assertNodeMigrationSecretBytes(t *testing.T, db *sql.DB) {
	t.Helper()
	var password, privateKey []byte
	if err := db.QueryRow(`SELECT password, private_key FROM nodes WHERE id = 101`).Scan(&password, &privateKey); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(password, []byte("enc:v2:known-password-bytes")) || !bytes.Equal(privateKey, []byte("enc:v1:known-key-bytes")) {
		t.Fatal("migration changed stored node credential bytes")
	}
}

func assertNodeProbeHistory(t *testing.T, db *sql.DB, status string, hasStatus bool) {
	t.Helper()
	var admitted, applied int
	var token, gotStatus string
	query := `SELECT admitted_seq, applied_seq, applied_token FROM node_probe_state WHERE node_id = 101`
	if hasStatus {
		query = `SELECT admitted_seq, applied_seq, applied_token, applied_status FROM node_probe_state WHERE node_id = 101`
	}
	dest := []any{&admitted, &applied, &token}
	if hasStatus {
		dest = append(dest, &gotStatus)
	}
	if err := db.QueryRow(query).Scan(dest...); err != nil {
		t.Fatal(err)
	}
	if admitted != 6 || applied != 6 || token != revisionMigrationProbeToken || gotStatus != status {
		t.Fatalf("probe history changed: %d %d %q %q", admitted, applied, token, gotStatus)
	}
}
