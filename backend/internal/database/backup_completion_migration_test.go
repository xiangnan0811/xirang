package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backuphealth"
)

func TestBackupCompletionFactsConcurrentExactReplayPostgres(t *testing.T) {
	fixture := newRequiredPostgresMigrationFixture(t)
	migrator, sqlDB := fixture.openAt(t, uint(backupCompletionFactsSchemaVersion))
	db := fixture.recoveryWorkerGorm(t, sqlDB)
	old := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	fixture.mustExec(t, sqlDB, `INSERT INTO nodes
		(id, name, host, port, username, auth_type, status, backup_dir, last_backup_at, created_at, updated_at)
		VALUES (1, 'completion-race-node', '127.0.0.1', 22, 'root', 'password', 'online', 'completion-race-node', ?, ?, ?)`,
		old, old, old)
	assertMigrationVersion(t, migrator, uint(backupCompletionFactsSchemaVersion))

	input := backuphealth.ManagedCommittedInput{
		TaskID: 1, TaskRunID: 1, NodeID: 1, ExecutorType: "restic",
		RecoveryPointID: strings.Repeat("c", 32),
		CommittedAt:     time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC),
	}
	const attempts = 8
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- backuphealth.RecordManagedCommitted(context.Background(), db, input)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact replay failed: %v", err)
		}
	}
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM backup_completions WHERE task_run_id = $1`, input.TaskRunID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent exact replay rows=%d, want 1", count)
	}
	var freshness time.Time
	if err := sqlDB.QueryRow(`SELECT last_backup_at FROM nodes WHERE id = $1`, input.NodeID).Scan(&freshness); err != nil {
		t.Fatal(err)
	}
	if !freshness.Equal(input.CommittedAt) {
		t.Fatalf("concurrent replay freshness=%s, want %s", freshness.UTC().Format(time.RFC3339), input.CommittedAt.UTC().Format(time.RFC3339))
	}
}
func TestBackupCompletionFactsMigrationSQLite(t *testing.T) {
	testBackupCompletionFactsMigration(t, newSQLiteMigrationFixture(t))
}

func TestBackupCompletionFactsMigrationPostgres(t *testing.T) {
	testBackupCompletionFactsMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testBackupCompletionFactsMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("backfills only immutable managed evidence and resets mutable freshness", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(alertDeliverySuccessSchemaVersion))
		seedBackupCompletionFactsMigrationRows(t, fixture, db)
		if err := migrator.Migrate(uint(backupCompletionFactsSchemaVersion)); err != nil {
			t.Fatalf("apply 000086/000087 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(backupCompletionFactsSchemaVersion))
		if err := validateMinimumRecoverySchema(db, fixture.engine, backupCompletionFactsSchemaVersion); err != nil {
			t.Fatalf("validate 000087 schema on %s: %v", fixture.engine, err)
		}

		var unverifiedKind, unverifiedStatus string
		var unverifiedCount int
		if err := db.QueryRow(fixture.bind(`SELECT fact_kind, evidence_status, COUNT(*)
			FROM backup_completions WHERE node_id = ? GROUP BY fact_kind, evidence_status`), 1801).Scan(&unverifiedKind, &unverifiedStatus, &unverifiedCount); err != nil {
			t.Fatalf("read historical fact on %s: %v", fixture.engine, err)
		}
		if unverifiedKind != "legacy_unverified" || unverifiedStatus != "unverified" || unverifiedCount != 1 {
			t.Fatalf("historical fact on %s = kind=%q status=%q count=%d, want one unverified row", fixture.engine, unverifiedKind, unverifiedStatus, unverifiedCount)
		}

		var managedKind, managedStatus, evidenceRef string
		var managedCompleted time.Time
		if err := db.QueryRow(fixture.bind(`SELECT fact_kind, evidence_status, evidence_ref, completed_at
			FROM backup_completions WHERE task_run_id = ?`), 3802).Scan(&managedKind, &managedStatus, &evidenceRef, &managedCompleted); err != nil {
			t.Fatalf("read managed fact on %s: %v", fixture.engine, err)
		}
		wantPointID := strings.Repeat("b", 32)
		wantCommitted := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
		if managedKind != "managed_committed" || managedStatus != "verified" || evidenceRef != wantPointID || !managedCompleted.Equal(wantCommitted) {
			t.Fatalf("managed fact on %s = kind=%q status=%q ref=%q completed=%s", fixture.engine, managedKind, managedStatus, evidenceRef, managedCompleted.UTC().Format(time.RFC3339))
		}

		importedPointID := strings.Repeat("c", 32)
		var importedKind, importedStatus string
		if err := db.QueryRow(fixture.bind(`SELECT fact_kind, evidence_status FROM backup_completions WHERE evidence_ref = ?`), importedPointID).
			Scan(&importedKind, &importedStatus); err != nil {
			t.Fatalf("read imported marker on %s: %v", fixture.engine, err)
		}
		if importedKind != "legacy_unverified" || importedStatus != "unverified" {
			t.Fatalf("imported point on %s = kind=%q status=%q, want unverified marker", fixture.engine, importedKind, importedStatus)
		}
		for _, poisonPointID := range []string{
			strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32),
		} {
			var markerKind, markerStatus string
			var markerTaskID, markerTaskRunID sql.NullInt64
			if err := db.QueryRow(fixture.bind(`SELECT fact_kind, evidence_status, task_id, task_run_id
				FROM backup_completions WHERE evidence_ref = ?`), poisonPointID).
				Scan(&markerKind, &markerStatus, &markerTaskID, &markerTaskRunID); err != nil {
				t.Fatalf("read poison marker %q on %s: %v", poisonPointID, fixture.engine, err)
			}
			if markerKind != "legacy_unverified" || markerStatus != "unverified" ||
				markerTaskID.Valid || markerTaskRunID.Valid {
				t.Fatalf("poison point %q on %s = kind=%q status=%q task=%v run=%v, want unverified marker without lineage",
					poisonPointID, fixture.engine, markerKind, markerStatus, markerTaskID, markerTaskRunID)
			}
		}

		fixture.expectExecRejected(t, db, `UPDATE backup_repositories SET provider_kind = ? WHERE id = ?`, "rclone", strings.Repeat("a", 32))
		var managedFreshness, historicalFreshness sql.NullTime
		if err := db.QueryRow(fixture.bind(`SELECT last_backup_at,
			(SELECT last_backup_at FROM nodes WHERE id = 1801) FROM nodes WHERE id = 1802`)).Scan(&managedFreshness, &historicalFreshness); err != nil {
			t.Fatalf("read rebuilt node freshness on %s: %v", fixture.engine, err)
		}
		if !managedFreshness.Valid || !managedFreshness.Time.Equal(wantCommitted) {
			t.Fatalf("managed node freshness on %s = %v, want %s", fixture.engine, managedFreshness, wantCommitted)
		}
		if historicalFreshness.Valid {
			t.Fatalf("historical-only node freshness on %s = %v, want NULL", fixture.engine, historicalFreshness)
		}
	})

	t.Run("immutable facts reject edits and used downgrade", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(backupCompletionFactsSchemaVersion))
		now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
		fixture.mustExec(t, db, `INSERT INTO backup_completions
			(node_id, fact_kind, evidence_status, completed_at, created_at, updated_at)
			VALUES (?, 'legacy_unverified', 'unverified', ?, ?, ?)`, 1, now, now, now)
		fixture.expectExecRejected(t, db, `UPDATE backup_completions SET completed_at = ? WHERE node_id = 1`, now.Add(time.Hour))
		fixture.expectExecRejected(t, db, `DELETE FROM backup_completions WHERE node_id = 1`)
		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("used 000087 downgrade on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, uint(backupCompletionFactsSchemaVersion))
		if !databaseTableExists(t, db, fixture.engine, "backup_completions") {
			t.Fatalf("used downgrade on %s removed backup_completions", fixture.engine)
		}
	})

	t.Run("pristine downgrade is reversible", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(backupCompletionFactsSchemaVersion))
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("pristine 000087 downgrade on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(taskCronOccurrenceResourceIdentitySchemaVersion))
		if databaseTableExists(t, db, fixture.engine, "backup_completions") {
			t.Fatalf("pristine downgrade on %s retained backup_completions", fixture.engine)
		}
	})

	t.Run("schema drift is refused before startup", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(backupCompletionFactsSchemaVersion))
		fixture.mustExec(t, db, `DROP INDEX idx_backup_completions_verified_task`)
		beforeVersion, beforeDirty, err := migrator.Version()
		if err != nil {
			t.Fatal(err)
		}
		if beforeVersion != uint(backupCompletionFactsSchemaVersion) || beforeDirty {
			t.Fatalf("preflight %s migration state = version=%d dirty=%v", fixture.engine, beforeVersion, beforeDirty)
		}
		err = RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
		if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "missing_backup_completion_verified_index") {
			t.Fatalf("schema drift on %s returned %v", fixture.engine, err)
		}
		afterVersion, afterDirty, err := migrator.Version()
		if err != nil {
			t.Fatal(err)
		}
		if afterVersion != beforeVersion || afterDirty != beforeDirty {
			t.Fatalf("schema drift on %s changed migration state to version=%d dirty=%v", fixture.engine, afterVersion, afterDirty)
		}
	})
}

func seedBackupCompletionFactsMigrationRows(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	old := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	captured := time.Date(2026, 9, 12, 8, 55, 0, 0, time.UTC)
	committed := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, port, username, auth_type, status, backup_dir, last_backup_at, created_at, updated_at)
		VALUES (1801, 'migration-history-only', '127.0.0.1', 22, 'root', 'password', 'online', 'migration-history-only', ?, ?, ?),
		       (1802, 'migration-managed', '127.0.0.1', 22, 'root', 'password', 'online', 'migration-managed', ?, ?, ?)`,
		old, old, old, old, old, old)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (2802, 'migration-managed-task', 1802, 'restic', 'active', ?, ?)`, old, old)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, trigger_type, status, node_id_snapshot, created_at, updated_at)
		VALUES (3802, 2802, 'manual', 'success', 1802, ?, ?)`, old, old)
	for runID := int64(3803); runID <= 3805; runID++ {
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, trigger_type, status, node_id_snapshot, created_at, updated_at)
			VALUES (?, 2802, 'manual', 'success', 1802, ?, ?)`, runID, old, old)
	}
	repositoryID := strings.Repeat("a", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_repositories
		(id, provider_kind, display_name, version_mode, status, capability_revision,
		 capabilities_json, immutability_level, created_at, updated_at)
		VALUES (?, 'restic', 'migration-repository', 'native_snapshot', 'online', 1, '{}', 'xirang_managed', ?, ?)`, repositoryID, old, old)
	pointID := strings.Repeat("b", 32)
	fixture.mustExec(t, db, `INSERT INTO recovery_points
		(id, repository_id, producing_task_id, producing_task_run_id,
		 producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
		 lineage_json, encrypted_provider_locator, encrypted_rollback_locator,
		 semantics, state, captured_at, committed_at, source_fingerprint,
		 manifest_digest_algorithm, manifest_digest, consistency_json, fidelity_json,
		 capabilities_json, immutability_level, physical_availability, hold_state,
		 created_at, updated_at)
		VALUES (?, ?, 2802, 3802, 'migration-managed-task', 1802, 'migration-managed',
		 '{"version":1,"task_repository_link_id":"dddddddddddddddddddddddddddddddd","task_id":2802,"task_run_id":3802,"trigger":"manual","publication_mode":"native_snapshot","point_codec_version":1,"tag_codec_version":1,"started_at":"2026-09-12T08:50:00Z","prepared_at":"2026-09-12T08:55:00Z","point_deadline_at":"2026-09-12T10:00:00Z"}', '', '', 'native_snapshot', 'committed', ?, ?, '',
		 'sha256', '', '{}', '{}', '{}', 'xirang_managed', 'online', 'none', ?, ?)`,
		pointID, repositoryID, captured, committed, old, old)
	poisonPoints := []struct {
		id      string
		runID   int64
		lineage string
	}{
		{id: strings.Repeat("d", 32), runID: 3803, lineage: `{not-json`},
		{id: strings.Repeat("e", 32), runID: 3804, lineage: `{"metadata":{"version":1,"task_repository_link_id":"dddddddddddddddddddddddddddddddd","task_id":2802,"task_run_id":3804,"trigger":"manual","publication_mode":"native_snapshot","point_codec_version":1,"tag_codec_version":1,"started_at":"2026-09-12T08:50:00Z","prepared_at":"2026-09-12T08:55:00Z","point_deadline_at":"2026-09-12T10:00:00Z"}}`},
		{id: strings.Repeat("f", 32), runID: 3805, lineage: `{"version":1,"version":1,"task_repository_link_id":"dddddddddddddddddddddddddddddddd","task_id":2802,"task_run_id":3805,"trigger":"manual","publication_mode":"native_snapshot","point_codec_version":1,"tag_codec_version":1,"started_at":"2026-09-12T08:50:00Z","prepared_at":"2026-09-12T08:55:00Z","point_deadline_at":"2026-09-12T10:00:00Z"}`},
	}
	for _, poison := range poisonPoints {
		fixture.mustExec(t, db, `INSERT INTO recovery_points
			(id, repository_id, producing_task_id, producing_task_run_id,
			 producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
			 lineage_json, encrypted_provider_locator, encrypted_rollback_locator,
			 semantics, state, captured_at, committed_at, source_fingerprint,
			 manifest_digest_algorithm, manifest_digest, consistency_json, fidelity_json,
			 capabilities_json, immutability_level, physical_availability, hold_state,
			 created_at, updated_at)
			VALUES (?, ?, 2802, ?, 'migration-managed-task', 1802, 'migration-managed',
				?, '', '', 'native_snapshot', 'committed', ?, ?, '',
				'sha256', '', '{}', '{}', '{}', 'xirang_managed', 'online', 'none', ?, ?)`,
			poison.id, repositoryID, poison.runID, poison.lineage, captured, committed, old, old)
	}
	importedPointID := strings.Repeat("c", 32)
	importedCaptured := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO recovery_points
		(id, repository_id, producing_node_id_snapshot, semantics, state, captured_at,
		 source_fingerprint, manifest_digest_algorithm, consistency_json, fidelity_json,
		 capability_revision, capabilities_json, immutability_level, physical_availability,
		 hold_state, created_at, updated_at)
		VALUES (?, ?, 1802, 'imported_baseline', 'committed', ?, '', 'sha256', '{}', '{}',
			1, '{}', 'backend_versioned', 'online', 'none', ?, ?)`,
		importedPointID, repositoryID, importedCaptured, importedCaptured, importedCaptured)
}
