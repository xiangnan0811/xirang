package database

import (
	"testing"
	"time"
)

func TestTaskCronOverrideMigrationBackfillsConservativelySQLite(t *testing.T) {
	testTaskCronOverrideMigrationBackfill(t, newSQLiteMigrationFixture(t))
}

func TestTaskCronOverrideMigrationBackfillsConservativelyPostgres(t *testing.T) {
	testTaskCronOverrideMigrationBackfill(t, newRequiredPostgresMigrationFixture(t))
}

func testTaskCronOverrideMigrationBackfill(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, uint(backupCompletionFactsSchemaVersion))
	now := time.Date(2026, 9, 14, 1, 2, 3, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO policies
		(id, name, source_path, target_path, cron_spec, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		88001, "override-active", "/source", "/target", "@every 1h", true, now, now)
	fixture.mustExec(t, db, `INSERT INTO policies
		(id, name, source_path, target_path, cron_spec, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		88002, "override-disabled", "/source-disabled", "/target-disabled", "@every 1h", false, now, now)

	insertTask := func(id, policyID int64, source, cron string) {
		fixture.mustExec(t, db, `INSERT INTO tasks
			(id, name, node_id, policy_id, executor_type, cron_spec, status, source, enabled, created_at, updated_at)
			VALUES (?, ?, 1, ?, 'rsync', ?, 'pending', ?, ?, ?, ?)`,
			id, "cron-override-task", policyID, cron, source, true, now, now)
	}
	insertTask(88011, 88001, "policy", "@every 1h")
	insertTask(88012, 88001, "policy", "@every 2h")
	insertTask(88013, 88001, "policy", "")
	insertTask(88014, 88002, "policy", "")
	insertTask(88015, 88002, "policy", "@every 2h")
	insertTask(88016, 88001, "manual", "@every 2h")

	if err := migrator.Steps(1); err != nil {
		t.Fatalf("apply 000088 %s: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, uint(taskCronOverrideSchemaVersion))

	rows, err := db.Query(fixture.bind(`SELECT id, cron_override FROM tasks WHERE id BETWEEN ? AND ? ORDER BY id`), 88011, 88016)
	if err != nil {
		t.Fatalf("read migrated %s task provenance: %v", fixture.engine, err)
	}
	defer func() { _ = rows.Close() }()
	want := map[int64]int64{
		88011: 0, // Equal active policy schedule: inherited.
		88012: 1, // Different active policy schedule: preserve as override.
		88013: 1, // Explicit-looking empty schedule while policy is active: preserve.
		88014: 0, // Empty schedule matches disabled policy's effective schedule.
		88015: 1, // Non-empty schedule differs from disabled policy's effective schedule.
		88016: 0, // Non-policy rows are outside policy inheritance.
	}
	for rows.Next() {
		var id int64
		var rawOverride interface{}
		if err := rows.Scan(&id, &rawOverride); err != nil {
			t.Fatalf("scan migrated %s task provenance: %v", fixture.engine, err)
		}
		override := int64(0)
		switch value := rawOverride.(type) {
		case int64:
			override = value
		case bool:
			if value {
				override = 1
			}
		default:
			t.Fatalf("scan migrated %s task provenance returned unsupported type %T", fixture.engine, rawOverride)
		}
		if override != want[id] {
			t.Fatalf("task %d migrated %s cron_override=%d, want %d", id, fixture.engine, override, want[id])
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated %s task provenance: %v", fixture.engine, err)
	}
	if len(want) != 0 {
		t.Fatalf("missing migrated %s task rows: %v", fixture.engine, want)
	}
}

func TestTaskCronOverrideMigrationDowngradeGuardSQLite(t *testing.T) {
	testTaskCronOverrideMigrationDowngradeGuard(t, newSQLiteMigrationFixture(t))
}

func TestTaskCronOverrideMigrationDowngradeGuardPostgres(t *testing.T) {
	testTaskCronOverrideMigrationDowngradeGuard(t, newRequiredPostgresMigrationFixture(t))
}

func testTaskCronOverrideMigrationDowngradeGuard(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, uint(taskCronOverrideSchemaVersion))
	now := time.Now().UTC()
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, cron_spec, cron_override, status, created_at, updated_at)
		VALUES (?, ?, 1, 'rsync', ?, ?, 'pending', ?, ?)`,
		88101, "used-cron-override", "@every 1h", true, now, now)
	if err := migrator.Steps(-1); err == nil {
		t.Fatalf("used 000088 %s downgrade unexpectedly succeeded", fixture.engine)
	}
	assertMigrationVersion(t, migrator, uint(taskCronOverrideSchemaVersion))
	if exists, err := migrationColumnExists(db, fixture.engine, "tasks", "cron_override"); err != nil {
		t.Fatalf("check preserved %s cron_override column: %v", fixture.engine, err)
	} else if !exists {
		t.Fatalf("rejected %s downgrade removed cron_override column", fixture.engine)
	}

	pristineMigrator, pristineDB := fixture.openAt(t, uint(taskCronOverrideSchemaVersion))
	if err := pristineMigrator.Steps(-1); err != nil {
		t.Fatalf("pristine 000088 %s downgrade: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, pristineMigrator, uint(backupCompletionFactsSchemaVersion))
	if exists, err := migrationColumnExists(pristineDB, fixture.engine, "tasks", "cron_override"); err != nil {
		t.Fatalf("check pristine %s cron_override column: %v", fixture.engine, err)
	} else if exists {
		t.Fatalf("pristine %s downgrade retained cron_override column", fixture.engine)
	}
}
