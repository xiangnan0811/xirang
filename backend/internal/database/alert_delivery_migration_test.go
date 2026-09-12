package database

import (
	"testing"
	"time"
)

func TestAlertDeliveryMigrationApplyDownSQLite(t *testing.T) {
	testAlertDeliveryMigrationApplyDown(t, newSQLiteMigrationFixture(t))
}

func TestAlertDeliveryMigrationApplyDownPostgres(t *testing.T) {
	testAlertDeliveryMigrationApplyDown(t, newRequiredPostgresMigrationFixture(t))
}

func testAlertDeliveryMigrationApplyDown(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, uint(taskRunRecoveryCaptureSchemaVersion))
	if err := migrator.Steps(2); err != nil {
		t.Fatalf("apply 000084/000085 on %s: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, uint(alertDeliverySuccessSchemaVersion))
	for table, columns := range map[string][]string{
		"alerts":           {"delivery_decision", "delivery_reason", "delivery_decided_at"},
		"alert_deliveries": {"decision", "delivery_key", "attempt_id", "lease_expires_at", "updated_at", "sent_at"},
	} {
		for _, column := range columns {
			if !databaseColumnExists(t, db, fixture.engine, table, column) {
				t.Fatalf("%s 000085 missing %s.%s after up", fixture.engine, table, column)
			}
		}
	}
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("pristine 000085 down on %s: %v", fixture.engine, err)
	}
	if got := migrationVersionForTest(t, migrator); got != uint(alertDeliveryIntentSchemaVersion) {
		t.Fatalf("%s version after 000085 down=%d, want %d", fixture.engine, got, alertDeliveryIntentSchemaVersion)
	}
	if databaseColumnExists(t, db, fixture.engine, "alert_deliveries", "sent_at") {
		t.Fatalf("%s 000085 left alert_deliveries.sent_at after down", fixture.engine)
	}
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("pristine 000084 down on %s: %v", fixture.engine, err)
	}
	if got := migrationVersionForTest(t, migrator); got != uint(taskRunRecoveryCaptureSchemaVersion) {
		t.Fatalf("%s version after pristine down=%d, want %d", fixture.engine, got, taskRunRecoveryCaptureSchemaVersion)
	}
	for table, columns := range map[string][]string{
		"alerts":           {"delivery_decision", "delivery_reason", "delivery_decided_at"},
		"alert_deliveries": {"decision", "delivery_key", "attempt_id", "lease_expires_at", "updated_at"},
	} {
		for _, column := range columns {
			if databaseColumnExists(t, db, fixture.engine, table, column) {
				t.Fatalf("%s 000084 left %s.%s after down", fixture.engine, table, column)
			}
		}
	}
}

func TestAlertDeliveryMigrationUsedDownRejectedSQLite(t *testing.T) {
	testAlertDeliveryMigrationUsedDownRejected(t, newSQLiteMigrationFixture(t))
}

func TestAlertDeliveryMigrationUsedDownRejectedPostgres(t *testing.T) {
	testAlertDeliveryMigrationUsedDownRejected(t, newRequiredPostgresMigrationFixture(t))
}

func testAlertDeliveryMigrationUsedDownRejected(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, uint(taskRunRecoveryCaptureSchemaVersion))
	if err := migrator.Steps(2); err != nil {
		t.Fatalf("apply 000084/000085 on %s: %v", fixture.engine, err)
	}
	now := time.Now().UTC()
	fixture.mustExec(t, db, `INSERT INTO alerts
		(node_id, node_name, severity, status, error_code, message, delivery_decision, delivery_decided_at, created_at, updated_at)
		VALUES (?, 'migration-node', 'critical', 'open', 'XR-MIGRATION-085', 'durable success', 'direct', ?, ?, ?)`, 1, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO alert_deliveries
		(alert_id, integration_id, status, decision, sent_at, created_at, updated_at)
		VALUES (1, 1, 'sent', 'deliver', ?, ?, ?)`, now, now, now)

	if err := migrator.Steps(-1); err == nil {
		t.Fatalf("used 000085 down on %s unexpectedly succeeded", fixture.engine)
	}
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration state after rejected 000085 down: %v", fixture.engine, err)
	}
	if version != uint(alertDeliverySuccessSchemaVersion) || dirty {
		t.Fatalf("rejected %s down changed migration state: version=%d dirty=%v", fixture.engine, version, dirty)
	}
	if !databaseColumnExists(t, db, fixture.engine, "alert_deliveries", "sent_at") {
		t.Fatalf("rejected %s down removed sent_at", fixture.engine)
	}

	fixture.mustExec(t, db, `UPDATE alert_deliveries SET sent_at = NULL WHERE alert_id = 1`)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("pristine 000085 down on %s: %v", fixture.engine, err)
	}
	if got := migrationVersionForTest(t, migrator); got != uint(alertDeliveryIntentSchemaVersion) {
		t.Fatalf("%s version after 000085 down=%d, want %d", fixture.engine, got, alertDeliveryIntentSchemaVersion)
	}
	if err := migrator.Steps(-1); err == nil {
		t.Fatalf("used 000084 down on %s unexpectedly succeeded", fixture.engine)
	}
	version, dirty, err = migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration state after rejected 000084 down: %v", fixture.engine, err)
	}
	if version != uint(alertDeliveryIntentSchemaVersion) || dirty {
		t.Fatalf("rejected %s 000084 down changed migration state: version=%d dirty=%v", fixture.engine, version, dirty)
	}
	if !databaseColumnExists(t, db, fixture.engine, "alerts", "delivery_decision") {
		t.Fatalf("rejected %s down removed delivery_decision", fixture.engine)
	}
}

func migrationVersionForTest(t *testing.T, migrator interface{ Version() (uint, bool, error) }) uint {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if dirty {
		t.Fatalf("migration is dirty at version %d", version)
	}
	return version
}
