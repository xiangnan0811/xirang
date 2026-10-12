package database

import (
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestBatchCommandRetryMigration(t *testing.T) {
	testBatchCommandRetryMigration(t, newSQLiteMigrationFixture(t))
}

func TestBatchCommandRetryMigrationPostgres(t *testing.T) {
	testBatchCommandRetryMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testBatchCommandRetryMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("pre receipt retained task still reserves after upgrade", func(t *testing.T) {
		migrator, sqlDB := fixture.openAt(t, 79)
		now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
		fixture.mustExec(t, sqlDB, `INSERT INTO nodes (id, name, host, username, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, 1, "legacy-retained-node", "127.0.0.1", "retry", now, now)
		fixture.mustExec(t, sqlDB, `INSERT INTO tasks
			(id, name, node_id, batch_id, source, command, executor_type, status, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'batch', ?, 'command', 'pending', TRUE, ?, ?)`,
			1, "legacy-retained-task", 1, "pre-receipt-batch", "printf legacy", now, now)
		if err := migrator.Migrate(uint(batchCommandRetrySchemaVersion)); err != nil {
			t.Fatalf("upgrade pre-receipt task on %s: %v", fixture.engine, err)
		}
		db := fixture.recoveryWorkerGorm(t, sqlDB)
		run := model.TaskRun{TaskID: 1, TriggerType: "manual", Status: model.TaskRunStatusPending}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("reserve upgraded historical task on %s: %v", fixture.engine, err)
		}
		if run.ID == 0 || run.NodeIDSnapshot != 1 || run.ExecutorTypeSnapshot != "command" {
			t.Fatalf("historical reservation lost task identity: %+v", run)
		}
		for _, table := range []string{"batch_commands", "batch_command_dispatches", "batch_command_run_evidences"} {
			var count int64
			if err := db.Table(table).Count(&count).Error; err != nil {
				t.Fatalf("read historical provenance on %s: %v", fixture.engine, err)
			}
			if count != 0 {
				t.Fatalf("upgrade or reservation invented %s rows: %d", table, count)
			}
		}
	})

	t.Run("apply validates empty schema", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion-1))
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply empty 000093 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
		if err := validateMinimumRecoverySchema(db, fixture.engine, batchCommandRetrySchemaVersion); err != nil {
			t.Fatalf("validate empty 000093 schema on %s: %v", fixture.engine, err)
		}
	})

	t.Run("apply preserves v0 rows and model parity", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion-1))
		legacyNow := time.Date(2026, 10, 12, 0, 1, 2, 0, time.UTC)
		fixture.mustExec(t, db, `INSERT INTO batch_commands
			(id, requester_id, idempotency_key, request_hash, retain, created_at, updated_at)
			VALUES (?, ?, ?, ?, FALSE, ?, ?)`,
			"legacy-batch", 1, "legacy-key", strings.Repeat("1", 64), legacyNow, legacyNow)
		fixture.mustExec(t, db, `INSERT INTO batch_command_dispatches
			(task_id, batch_id, node_id, status, run_id, last_error, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 0, '', ?, ?)`,
			9001, "legacy-batch", 1, legacyNow, legacyNow)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply 000093 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
		if err := validateMinimumRecoverySchema(db, fixture.engine, batchCommandRetrySchemaVersion); err != nil {
			t.Fatalf("validate 000093 schema on %s: %v", fixture.engine, err)
		}
		var legacyVersion int
		var legacySource sql.NullString
		if err := db.QueryRow(fixture.bind(`SELECT provenance_version, source_batch_id
			FROM batch_commands WHERE id = ?`), "legacy-batch").Scan(&legacyVersion, &legacySource); err != nil {
			t.Fatalf("read migrated legacy batch on %s: %v", fixture.engine, err)
		}
		if legacyVersion != 0 || legacySource.Valid {
			t.Fatalf("legacy batch on %s was promoted or sourced: version=%d source=%v", fixture.engine, legacyVersion, legacySource)
		}
		var legacyFingerprint string
		if err := db.QueryRow(fixture.bind(`SELECT command_fingerprint
			FROM batch_command_dispatches WHERE task_id = ?`), 9001).Scan(&legacyFingerprint); err != nil {
			t.Fatalf("read migrated legacy dispatch on %s: %v", fixture.engine, err)
		}
		if legacyFingerprint != "" {
			t.Fatalf("legacy dispatch on %s received a command fingerprint %q", fixture.engine, legacyFingerprint)
		}
		assertBatchCommandRetryModelParity(t, fixture, db)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("downgrade unused 000093 with v0 rows on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion-1))
		var legacyCount int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM batch_commands WHERE id = ?`), "legacy-batch").Scan(&legacyCount); err != nil {
			t.Fatalf("read legacy batch after downgrade on %s: %v", fixture.engine, err)
		}
		if legacyCount != 1 {
			t.Fatalf("unused 000093 downgrade removed v0 batch on %s", fixture.engine)
		}
	})

	t.Run("pristine down is reversible", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("pristine 000093 down on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion-1))
		for _, table := range []string{"batch_command_run_evidences"} {
			if databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("pristine 000093 down on %s retained %s", fixture.engine, table)
			}
		}
		for _, column := range []struct{ table, column string }{
			{table: "batch_commands", column: "provenance_version"},
			{table: "batch_commands", column: "source_batch_id"},
			{table: "batch_command_dispatches", column: "command_fingerprint"},
			{table: "batch_command_dispatches", column: "retry_of_task_id"},
			{table: "batch_command_dispatches", column: "retry_of_run_id"},
		} {
			if exists, err := migrationColumnExists(db, fixture.engine, column.table, column.column); err != nil {
				t.Fatalf("check removed %s.%s on %s: %v", column.table, column.column, fixture.engine, err)
			} else if exists {
				t.Fatalf("pristine 000093 down on %s retained %s.%s", fixture.engine, column.table, column.column)
			}
		}
	})

	t.Run("v1 batch admission blocks used downgrade atomically", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		now := time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)
		fixture.mustExec(t, db, `INSERT INTO batch_commands
			(id, requester_id, idempotency_key, request_hash, provenance_version, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?)`,
			"retry-used-batch", 1, "retry-used-key", strings.Repeat("a", 64), now, now)
		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("used 000093 downgrade on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
		if !databaseTableExists(t, db, fixture.engine, "batch_command_run_evidences") {
			t.Fatalf("rejected 000093 downgrade on %s removed evidence table", fixture.engine)
		}
		var count int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM batch_commands WHERE id = ?`), "retry-used-batch").Scan(&count); err != nil {
			t.Fatalf("count used v1 batch on %s: %v", fixture.engine, err)
		}
		if count != 1 {
			t.Fatalf("used v1 batch disappeared on %s", fixture.engine)
		}
	})

	t.Run("source edges and evidence transitions are constrained", func(t *testing.T) {
		_, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		now := time.Date(2026, 10, 12, 2, 3, 4, 0, time.UTC)
		fingerprint := strings.Repeat("b", 64)
		fixture.mustExec(t, db, `INSERT INTO batch_commands
			(id, requester_id, idempotency_key, request_hash, provenance_version, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?)`,
			"retry-source-batch", 1, "retry-source-key", strings.Repeat("c", 64), now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO batch_command_dispatches
			(task_id, batch_id, node_id, status, command_fingerprint, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?, ?)`, 1, "retry-source-batch", 1, "not-a-fingerprint", now, now)
		fixture.mustExec(t, db, `INSERT INTO batch_command_dispatches
			(task_id, batch_id, node_id, status, command_fingerprint, created_at, updated_at)
			VALUES (?, ?, ?, 'accepted', ?, ?, ?)`, 1, "retry-source-batch", 1, fingerprint, now, now)

		fixture.mustExec(t, db, `INSERT INTO batch_commands
			(id, requester_id, idempotency_key, request_hash, provenance_version, source_batch_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
			"retry-child-batch", 1, "retry-child-key", strings.Repeat("d", 64), "retry-source-batch", now, now)
		fixture.mustExec(t, db, `INSERT INTO batch_command_dispatches
			(task_id, batch_id, node_id, status, command_fingerprint, retry_of_task_id, retry_of_run_id, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, ?)`, 2, "retry-child-batch", 1, strings.Repeat("e", 64), 1, 0, now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO batch_command_dispatches
			(task_id, batch_id, node_id, status, command_fingerprint, retry_of_task_id, retry_of_run_id, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, ?)`, 3, "retry-child-batch", 2, strings.Repeat("f", 64), 1, 0, now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO batch_command_dispatches
			(task_id, batch_id, node_id, status, command_fingerprint, retry_of_task_id, retry_of_run_id, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, ?)`, 4, "retry-child-batch", 1, strings.Repeat("f", 64), 1, 0, now, now)

		fixture.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, 1, "retry-node", "127.0.0.1", "retry", now, now)
		fixture.mustExec(t, db, `INSERT INTO tasks
			(id, name, node_id, batch_id, source, command, executor_type, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'batch', ?, 'command', 'pending', ?, ?)`,
			1, "retry-task", 1, "retry-source-batch", "false", now, now)
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, node_id_snapshot, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`, 11, 1, 1, now, now)
		fixture.mustExec(t, db, `INSERT INTO batch_command_run_evidences
			(run_id, task_id, node_id, command_fingerprint, outcome, created_at)
			VALUES (?, ?, ?, ?, 'pending', ?)`, 11, 1, 1, fingerprint, now)
		fixture.mustExec(t, db, `INSERT INTO tasks
			(id, name, node_id, batch_id, source, command, executor_type, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'batch', ?, 'command', 'pending', ?, ?)`,
			2, "retry-child-task", 1, "retry-child-batch", "false", now, now)
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, node_id_snapshot, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`, 12, 2, 1, now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO batch_command_run_evidences
			(run_id, task_id, node_id, command_fingerprint, outcome, created_at)
			VALUES (?, ?, ?, ?, 'pending', ?)`, 12, 2, 1, strings.Repeat("f", 64), now)
		fixture.expectExecRejected(t, db, `INSERT INTO batch_command_run_evidences
			(run_id, task_id, node_id, command_fingerprint, outcome, created_at)
			VALUES (?, ?, ?, ?, 'pending', ?)`, 13, 2, 2, strings.Repeat("e", 64), now)
		fixture.mustExec(t, db, `INSERT INTO batch_command_run_evidences
			(run_id, task_id, node_id, command_fingerprint, outcome, created_at)
			VALUES (?, ?, ?, ?, 'pending', ?)`, 12, 2, 1, strings.Repeat("e", 64), now)
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, node_id_snapshot, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`, 14, 1, 1, now, now)
		fixture.mustExec(t, db, `INSERT INTO batch_command_run_evidences
			(run_id, task_id, node_id, command_fingerprint, outcome, created_at)
			VALUES (?, ?, ?, '', 'pending', ?)`, 14, 1, 1, now)
		fixture.mustExec(t, db, `UPDATE task_runs SET status = 'failed', finished_at = ? WHERE id = ?`, now, 14)
		fixture.expectExecRejected(t, db, `UPDATE batch_command_run_evidences
			SET outcome = 'exit_failure', exit_code = 7, finished_at = ? WHERE run_id = ?`, now, 14)
		fixture.mustExec(t, db, `UPDATE batch_command_run_evidences
			SET outcome = 'unknown', finished_at = ? WHERE run_id = ?`, now, 14)
		fixture.expectExecRejected(t, db, `UPDATE batch_command_run_evidences
			SET node_id = 2 WHERE run_id = ?`, 11)
		fixture.expectExecRejected(t, db, `UPDATE batch_command_run_evidences
			SET outcome = 'exit_failure', exit_code = 0, finished_at = ? WHERE run_id = ?`, now, 11)
		fixture.expectExecRejected(t, db, `UPDATE batch_command_run_evidences
			SET outcome = 'exit_failure', exit_code = 7, finished_at = ? WHERE run_id = ?`, now, 11)
		fixture.mustExec(t, db, `UPDATE task_runs SET status = 'failed', finished_at = ? WHERE id = ?`, now, 11)
		fixture.mustExec(t, db, `UPDATE batch_command_run_evidences
			SET outcome = 'exit_failure', exit_code = 7, finished_at = ? WHERE run_id = ?`, now, 11)
		fixture.expectExecRejected(t, db, `UPDATE batch_command_run_evidences SET outcome = 'unknown' WHERE run_id = ?`, 11)
		fixture.mustExec(t, db, `DELETE FROM task_runs WHERE id = ?`, 11)
		var evidenceCount int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM batch_command_run_evidences WHERE run_id = ?`), 11).Scan(&evidenceCount); err != nil {
			t.Fatalf("count evidence after TaskRun cleanup on %s: %v", fixture.engine, err)
		}
		if evidenceCount != 1 {
			t.Fatalf("evidence row was cascaded with TaskRun on %s", fixture.engine)
		}
		fixture.expectExecRejected(t, db, `DELETE FROM batch_command_run_evidences WHERE run_id = ?`, 11)
		fixture.expectExecRejected(t, db, `DELETE FROM batch_command_dispatches WHERE task_id = ?`, 1)
	})
	t.Run("real TaskRun accepts edited and unsupported root snapshots", func(t *testing.T) {
		_, sqlDB := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		db := fixture.recoveryWorkerGorm(t, sqlDB)
		cases := []struct {
			batchID        string
			initialCommand string
			editedCommand  string
			editedExecutor string
			editedNode     uint
		}{
			{batchID: "root-edited-command", initialCommand: "printf original", editedCommand: "printf edited", editedExecutor: "command", editedNode: 1},
			{batchID: "root-empty-shape", initialCommand: "printf original-empty", editedCommand: "", editedExecutor: "local", editedNode: 2},
		}
		for _, testCase := range cases {
			t.Run(testCase.batchID, func(t *testing.T) {
				batch := model.BatchCommand{
					ID:                testCase.batchID,
					RequesterID:       1,
					IdempotencyKey:    testCase.batchID + "-key",
					RequestHash:       strings.Repeat("1", 64),
					ProvenanceVersion: 1,
				}
				if err := db.Create(&batch).Error; err != nil {
					t.Fatalf("create root batch on %s: %v", fixture.engine, err)
				}
				task := model.Task{
					Name:         "task-" + testCase.batchID,
					NodeID:       1,
					ExecutorType: "command",
					Command:      testCase.initialCommand,
					Source:       "batch",
					Status:       "pending",
					BatchID:      batch.ID,
					Enabled:      true,
				}
				if err := db.Create(&task).Error; err != nil {
					t.Fatalf("create root task on %s: %v", fixture.engine, err)
				}
				originalFingerprint := model.BatchCommandTaskFingerprint(task)
				if originalFingerprint == "" {
					t.Fatal("root fixture command fingerprint is empty")
				}
				dispatch := model.BatchCommandDispatch{
					TaskID:             task.ID,
					BatchID:            batch.ID,
					NodeID:             task.NodeID,
					Status:             "pending",
					CommandFingerprint: originalFingerprint,
				}
				if err := db.Create(&dispatch).Error; err != nil {
					t.Fatalf("create root dispatch on %s: %v", fixture.engine, err)
				}
				if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
					"command":       testCase.editedCommand,
					"executor_type": testCase.editedExecutor,
					"node_id":       testCase.editedNode,
				}).Error; err != nil {
					t.Fatalf("edit root task on %s: %v", fixture.engine, err)
				}
				var currentTask model.Task
				if err := db.First(&currentTask, "id = ?", task.ID).Error; err != nil {
					t.Fatalf("read edited root task on %s: %v", fixture.engine, err)
				}
				currentFingerprint := model.BatchCommandTaskFingerprint(currentTask)
				run := model.TaskRun{TaskID: task.ID, TriggerType: "manual", Status: "pending"}
				if err := db.Create(&run).Error; err != nil {
					t.Fatalf("create edited root TaskRun on %s: %v", fixture.engine, err)
				}
				if run.BatchCommandEvidence == nil {
					t.Fatal("root TaskRun did not prepare transient evidence")
				}
				var evidence model.BatchCommandRunEvidence
				if err := db.First(&evidence, "run_id = ?", run.ID).Error; err != nil {
					t.Fatalf("read root TaskRun evidence on %s: %v", fixture.engine, err)
				}
				if evidence.TaskID != task.ID || evidence.NodeID != run.NodeIDSnapshot ||
					evidence.Outcome != "pending" || evidence.CommandFingerprint != currentFingerprint {
					t.Fatalf("root evidence on %s=%+v current fingerprint=%q run=%+v", fixture.engine, evidence, currentFingerprint, run)
				}
				if testCase.editedCommand != "" && evidence.CommandFingerprint == originalFingerprint {
					t.Fatal("edited root evidence retained the immutable dispatch fingerprint")
				}
				if testCase.editedCommand == "" && evidence.CommandFingerprint != "" {
					t.Fatalf("unsupported root evidence fingerprint=%q, want empty", evidence.CommandFingerprint)
				}
			})
		}
	})

	t.Run("same-name semantic no-op guard is schema drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION batch_command_retry_batch_guard()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_batch_command_retry_batch_insert`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_batch_command_retry_batch_insert
				BEFORE INSERT ON batch_commands BEGIN SELECT 1; END`)
		}
		err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
		if !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("same-name no-op retry guard on %s returned %v, want ErrMigrationSchemaDrift", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
	})
	t.Run("fragment-preserving guard bypass is schema drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		if fixture.engine == "postgres" {
			definition, err := migrationTriggerFunctionDefinition(db, "batch_commands", "trg_batch_command_retry_batch_insert")
			if err != nil {
				t.Fatalf("read PostgreSQL retry guard: %v", err)
			}
			body, ok := migrationPostgresFunctionBody(definition)
			if !ok {
				t.Fatal("batch guard function body missing from PostgreSQL catalog")
			}
			beginAt := strings.Index(strings.ToLower(body), "begin")
			if beginAt < 0 {
				t.Fatal("batch guard BEGIN missing from PostgreSQL retry migration")
			}
			mutated := body[:beginAt+len("begin")] + "\n    IF TRUE THEN RETURN NEW; END IF;" + body[beginAt+len("begin"):]
			fixture.mustExec(t, db, "CREATE OR REPLACE FUNCTION batch_command_retry_batch_guard() RETURNS trigger LANGUAGE plpgsql AS $$"+mutated+"$$")
		} else {
			definition, err := migrationTriggerDefinition(db, fixture.engine, "batch_commands", "trg_batch_command_retry_batch_insert")
			if err != nil {
				t.Fatalf("read SQLite batch guard: %v", err)
			}
			whenAt := strings.Index(strings.ToLower(definition), "when")
			if whenAt < 0 {
				t.Fatal("batch guard WHEN missing from SQLite retry migration")
			}
			mutated := definition[:whenAt] + "WHEN 1 = 0 AND " + definition[whenAt+len("when"):]
			fixture.mustExec(t, db, `DROP TRIGGER trg_batch_command_retry_batch_insert`)
			fixture.mustExec(t, db, mutated)
		}
		err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
		if !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("fragment-preserving retry guard bypass on %s returned %v, want ErrMigrationSchemaDrift", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
	})

	t.Run("same-name no-op downgrade admission is schema drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION batch_command_retry_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_batch_command_retry_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_batch_command_retry_downgrade_admission
				BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`)
		}
		err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
		if !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("same-name no-op downgrade admission on %s returned %v, want ErrMigrationSchemaDrift", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
	})

	t.Run("missing retry source index is schema drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(batchCommandRetrySchemaVersion))
		fixture.mustExec(t, db, `DROP INDEX idx_batch_command_dispatches_retry_source`)
		err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
		if !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("missing retry source index on %s returned %v, want ErrMigrationSchemaDrift", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(batchCommandRetrySchemaVersion))
	})

	t.Run("paired migration files are present", func(t *testing.T) {
		for _, path := range []string{
			"migrations/sqlite/000093_batch_command_retry.up.sql",
			"migrations/sqlite/000093_batch_command_retry.down.sql",
		} {
			if _, err := sqliteMigrationsFS.ReadFile(path); err != nil {
				t.Fatalf("read SQLite retry migration %s: %v", path, err)
			}
		}
		for _, path := range []string{
			"migrations/postgres/000093_batch_command_retry.up.sql",
			"migrations/postgres/000093_batch_command_retry.down.sql",
		} {
			if _, err := postgresMigrationsFS.ReadFile(path); err != nil {
				t.Fatalf("read PostgreSQL retry migration %s: %v", path, err)
			}
		}
	})
}

func assertBatchCommandRetryModelParity(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	models := map[string]any{
		"batch_commands":              model.BatchCommand{},
		"batch_command_dispatches":    model.BatchCommandDispatch{},
		"batch_command_run_evidences": model.BatchCommandRunEvidence{},
	}
	for table, persistentModel := range models {
		want := gormColumnNames(t, persistentModel)
		var got []string
		if fixture.engine == "sqlite" {
			got = sqliteColumnNames(t, db, table)
		} else {
			got = postgresColumnNames(t, db, table)
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s %s model columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
		}
	}
}
