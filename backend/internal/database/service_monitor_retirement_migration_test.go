package database

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const serviceMonitorRetirementMigrationVersion uint = 91

func TestServiceMonitorRetirementMigrationSQLite(t *testing.T) {
	testServiceMonitorRetirementMigration(t, newSQLiteMigrationFixture(t))
}

func TestServiceMonitorRetirementMigrationPostgres(t *testing.T) {
	testServiceMonitorRetirementMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testServiceMonitorRetirementMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("mixed service alerts and deliveries are fenced before tables disappear", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupFocusRetirementMigrationVersion-1)
		seedBackupFocusRetirementMixedData(t, fixture, db)
		seedServiceMonitorRetirementMixedData(t, fixture, db)
		seedServiceMonitorRetirementPreservedData(t, fixture, db)

		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply 000090 on %s: %v", fixture.engine, err)
		}
		preservationBefore := captureServiceMonitorRetirementPreservedData(t, fixture, db)
		sentDeliveriesBefore := snapshotServiceMonitorRetirementRows(t, fixture, db,
			`SELECT * FROM alert_deliveries WHERE id IN (?, ?) ORDER BY id`, 915, 919)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply 000091 on %s: %v", fixture.engine, err)
		}
		preservationAfter := captureServiceMonitorRetirementPreservedData(t, fixture, db)
		sentDeliveriesAfter := snapshotServiceMonitorRetirementRows(t, fixture, db,
			`SELECT * FROM alert_deliveries WHERE id IN (?, ?) ORDER BY id`, 915, 919)
		if !reflect.DeepEqual(sentDeliveriesBefore, sentDeliveriesAfter) {
			t.Fatalf("%s sent delivery rows changed across service-monitor retirement: before=%v after=%v", fixture.engine, sentDeliveriesBefore, sentDeliveriesAfter)
		}
		assertServiceMonitorRetirementPreservedData(t, fixture, preservationBefore, preservationAfter)
		assertMigrationVersion(t, migrator, serviceMonitorRetirementMigrationVersion)
		assertServiceMonitorRetirementSchema(t, fixture, db)
		assertBackupFocusRetirementData(t, fixture, db)
		assertServiceMonitorRetirementData(t, fixture, db)

		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("000091 Steps(-1) on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, serviceMonitorRetirementMigrationVersion)
		assertServiceMonitorRetirementSchema(t, fixture, db)
		assertBackupFocusRetirementData(t, fixture, db)
		assertServiceMonitorRetirementData(t, fixture, db)
		assertServiceMonitorRetirementPreservedData(t, fixture, preservationBefore, captureServiceMonitorRetirementPreservedData(t, fixture, db))
		sentDeliveriesAfterDown := snapshotServiceMonitorRetirementRows(t, fixture, db,
			`SELECT * FROM alert_deliveries WHERE id IN (?, ?) ORDER BY id`, 915, 919)
		if !reflect.DeepEqual(sentDeliveriesBefore, sentDeliveriesAfterDown) {
			t.Fatalf("%s sent delivery rows changed after rejected service-monitor downgrade: before=%v after=%v", fixture.engine, sentDeliveriesBefore, sentDeliveriesAfterDown)
		}
	})

	t.Run("empty schema has an irreversible clean head", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupFocusRetirementMigrationVersion-1)
		if err := migrator.Steps(2); err != nil {
			t.Fatalf("apply empty 000090 and 000091 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, serviceMonitorRetirementMigrationVersion)
		assertServiceMonitorRetirementSchema(t, fixture, db)

		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("empty 000091 Steps(-1) on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, serviceMonitorRetirementMigrationVersion)
		assertServiceMonitorRetirementSchema(t, fixture, db)
	})
}

func seedServiceMonitorRetirementMixedData(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now, failedSentAt, historicalSentAt, existingDecisionAt := serviceMonitorRetirementFixtureTimes()
	future := now.Add(time.Hour)

	fixture.mustExec(t, db, `INSERT INTO service_monitors
		(id, name, description, type, target, interval_seconds, timeout_seconds, http_method,
		 http_expected_status, http_headers, enabled, last_status, uptime_pct, created_at, updated_at)
		VALUES (1, 'retirement-http-plaintext', 'retirement fixture', 'http', 'https://example.invalid', 60, 10,
		 'GET', 200, 'X-Token: plaintext', TRUE, 'down', 0, ?, ?),
		       (2, 'retirement-http-v1-invalid', 'retirement fixture', 'http', 'https://example.invalid/v1', 60, 10,
		 'GET', 200, 'enc:v1:invalid', FALSE, 'unknown', 0, ?, ?),
		       (3, 'retirement-tcp-v2-invalid', 'retirement fixture', 'tcp', 'example.invalid:443', 60, 10,
		 'GET', 200, 'enc:v2:invalid', TRUE, 'unknown', 0, ?, ?)`, now, now, now, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO service_uptime_samples
		(id, monitor_id, hour, probe_count, probe_ok)
		VALUES (1, 1, ?, 4, 1), (2, 2, ?, 2, 2), (3, 3, ?, 1, 0), (4, 999, ?, 8, 8)`, now, now, now, now)

	for _, alert := range []struct {
		id      int
		status  string
		code    string
		retry   bool
		message string
	}{
		{901, "open", "XR-SERVICE-DOWN-1", true, "retired service 1"},
		{902, "acked", "XR-SERVICE-DOWN-999", true, "orphan retired service"},
		{903, "resolved", "XR-SERVICE-DOWN-7", true, "already resolved service"},
		{904, "open", "XR-SERVICE-DOWN-", true, "malformed service code"},
		{905, "acked", "XR-SERVICE-DOWN-1abc", true, "non-numeric service code"},
		{906, "resolved", "XR-OTHER-1", true, "unrelated source"},
	} {
		fixture.mustExec(t, db, `INSERT INTO alerts
			(id, node_id, node_name, severity, status, error_code, message, retryable, created_at, updated_at)
			VALUES (?, 0, 'service-retirement-fixture', 'warning', ?, ?, ?, ?, ?, ?)`,
			alert.id, alert.status, alert.code, alert.message, alert.retry, now, now)
	}
	fixture.mustExec(t, db, `UPDATE alerts
		SET delivery_decision = 'deliver', delivery_reason = 'legacy-history', delivery_decided_at = ?
		WHERE id = 901`, existingDecisionAt)

	insertDelivery := func(id, alertID int, status string, sentAt any) {
		lease := any(future)
		nextRetry := any(future)
		if status == "failed" || status == "sent" {
			lease = nil
			nextRetry = nil
		}
		fixture.mustExec(t, db, `INSERT INTO alert_deliveries
			(id, alert_id, integration_id, status, attempt_count, next_retry_at, last_error,
			 decision, delivery_key, attempt_id, lease_expires_at, sent_at, created_at, updated_at)
			VALUES (?, ?, 1, ?, ?, ?, ?, 'deliver', ?, ?, ?, ?, ?, ?)`,
			id, alertID, status, id-900, nextRetry, "delivery-error", "service-key-"+strconv.Itoa(id), "service-attempt-"+strconv.Itoa(id), lease, sentAt, now, now)
	}
	insertDelivery(911, 901, "pending", nil)
	insertDelivery(912, 902, "sending", nil)
	insertDelivery(913, 903, "retrying", nil)
	insertDelivery(914, 901, "failed", failedSentAt)
	insertDelivery(915, 901, "sent", nil)
	insertDelivery(916, 904, "failed", nil)
	insertDelivery(917, 905, "retrying", nil)
	insertDelivery(918, 906, "pending", nil)
	insertDelivery(919, 901, "sent", historicalSentAt)
}

func serviceMonitorRetirementFixtureTimes() (now, failedSentAt, historicalSentAt, existingDecisionAt time.Time) {
	now = time.Date(2026, 10, 1, 2, 3, 4, 0, time.UTC)
	failedSentAt = now.Add(-15 * time.Minute)
	historicalSentAt = now.Add(-30 * time.Minute)
	existingDecisionAt = now.Add(-2 * time.Hour)
	return now, failedSentAt, historicalSentAt, existingDecisionAt
}

func seedServiceMonitorRetirementPreservedData(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 10, 1, 2, 3, 4, 0, time.UTC)
	taskID := int64(500)
	taskRunID := int64(501)
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)

	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: pointID, RepositoryID: repositoryID, TaskID: &taskID, TaskRunID: &taskRunID,
		Semantics: "native_snapshot", State: "committed", SourceFingerprint: "service-retention-point",
	})
	fixture.mustExec(t, db, `UPDATE recovery_points
		SET producing_task_name_snapshot = 'retained-task',
		 producing_node_id_snapshot = 1,
		 producing_node_name_snapshot = 'retirement-node',
		 lineage_json = '{"source":"service-retention"}',
		 encrypted_provider_locator = 'enc:v2:provider-retention',
		 encrypted_rollback_locator = 'enc:v2:rollback-retention'
		WHERE id = ?`, pointID)
	fixture.mustExec(t, db, `INSERT INTO backup_completions
		(id, task_id, task_run_id, node_id, executor_type, fact_kind, evidence_status,
		 completed_at, evidence_ref, created_at, updated_at)
		VALUES (601, 500, 501, 1, 'restic', 'managed_committed', 'verified', ?, ?, ?, ?)`,
		now, pointID, now, now)
}

func assertServiceMonitorRetirementSchema(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"service_monitors", "service_uptime_samples"} {
		if databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s retired table %s remains", fixture.engine, table)
		}
	}
	if !fixture.recoveryTriggerExists(t, db, "schema_migrations", serviceMonitorRetirementAdmissionTrigger) {
		t.Fatalf("%s service retirement insert/update admission trigger is missing", fixture.engine)
	}
	if fixture.engine == "sqlite" && !fixture.recoveryTriggerExists(t, db, "schema_migrations", serviceMonitorRetirementUpdateAdmissionTrigger) {
		t.Fatalf("SQLite service retirement update admission trigger is missing")
	}
}

func assertServiceMonitorRetirementData(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	_, _, historicalSentAt, existingDecisionAt := serviceMonitorRetirementFixtureTimes()
	failedSentAt := historicalSentAt.Add(15 * time.Minute)
	for id, want := range map[int]struct {
		status             string
		retryable          bool
		retired            bool
		expectedDecisionAt time.Time
	}{
		901: {status: "resolved", retryable: false, retired: true, expectedDecisionAt: existingDecisionAt},
		902: {status: "resolved", retryable: false, retired: true},
		903: {status: "resolved", retryable: false, retired: true},
		904: {status: "open", retryable: true, retired: false},
		905: {status: "acked", retryable: true, retired: false},
		906: {status: "resolved", retryable: true, retired: false},
	} {
		var status string
		var retryable bool
		var decision, reason sql.NullString
		var decidedAt sql.NullTime
		if err := db.QueryRow(fixture.bind(`SELECT status, retryable, delivery_decision, delivery_reason, delivery_decided_at FROM alerts WHERE id = ?`), id).
			Scan(&status, &retryable, &decision, &reason, &decidedAt); err != nil {
			t.Fatalf("read alert on %s: %v", fixture.engine, err)
		}
		if want.retired {
			if status != want.status || retryable != want.retryable || !decision.Valid || decision.String != "unknown" || !reason.Valid || reason.String != "feature_retired" || !decidedAt.Valid {
				t.Fatalf("%s retired alert changed: status=%q retryable=%t decision=%q reason=%q decided_at=%t", fixture.engine, status, retryable, decision.String, reason.String, decidedAt.Valid)
			}
			if !want.expectedDecisionAt.IsZero() && (!decidedAt.Valid || !decidedAt.Time.Equal(want.expectedDecisionAt)) {
				t.Fatalf("%s alert %d changed existing delivery_decided_at: got=%s want=%s", fixture.engine, id, decidedAt.Time.UTC().Format(time.RFC3339Nano), want.expectedDecisionAt.Format(time.RFC3339Nano))
			}
		} else if status != want.status || retryable != want.retryable || decision.Valid || reason.Valid || decidedAt.Valid {
			t.Fatalf("%s non-matching alert changed: status=%q retryable=%t decision_valid=%t reason_valid=%t decided_at=%t", fixture.engine, status, retryable, decision.Valid, reason.Valid, decidedAt.Valid)
		}
	}

	expectations := map[int]struct {
		status         string
		decision       string
		key            string
		attemptID      string
		attemptCount   int
		lease          bool
		nextRetry      bool
		sent           bool
		expectedSentAt time.Time
	}{
		911: {status: "failed", decision: "unknown", key: "service-key-911", attemptID: "service-attempt-911", attemptCount: 11},
		912: {status: "failed", decision: "unknown", key: "service-key-912", attemptID: "service-attempt-912", attemptCount: 12},
		913: {status: "failed", decision: "unknown", key: "service-key-913", attemptID: "service-attempt-913", attemptCount: 13},
		914: {status: "failed", decision: "unknown", key: "service-key-914", attemptID: "service-attempt-914", attemptCount: 14, sent: true, expectedSentAt: failedSentAt},
		915: {status: "sent", decision: "deliver", key: "service-key-915", attemptID: "service-attempt-915", attemptCount: 15},
		916: {status: "failed", decision: "deliver", key: "service-key-916", attemptID: "service-attempt-916", attemptCount: 16},
		917: {status: "retrying", decision: "deliver", key: "service-key-917", attemptID: "service-attempt-917", attemptCount: 17, lease: true, nextRetry: true},
		918: {status: "pending", decision: "deliver", key: "service-key-918", attemptID: "service-attempt-918", attemptCount: 18, lease: true, nextRetry: true},
		919: {status: "sent", decision: "deliver", key: "service-key-919", attemptID: "service-attempt-919", attemptCount: 19, expectedSentAt: historicalSentAt},
	}
	for id, want := range expectations {
		var status, decision, key, attemptID, lastError string
		var attemptCount int
		var lease, nextRetry, sentAt sql.NullTime
		if err := db.QueryRow(fixture.bind(`SELECT status, decision, delivery_key, attempt_id, attempt_count, last_error, lease_expires_at, next_retry_at, sent_at FROM alert_deliveries WHERE id = ?`), id).
			Scan(&status, &decision, &key, &attemptID, &attemptCount, &lastError, &lease, &nextRetry, &sentAt); err != nil {
			t.Fatalf("read delivery %d on %s: %v", id, fixture.engine, err)
		}
		sentAtMatches := sentAt.Valid == want.sent
		if !want.expectedSentAt.IsZero() {
			sentAtMatches = sentAt.Valid && sentAt.Time.Equal(want.expectedSentAt)
		}
		if status != want.status || decision != want.decision || key != want.key || attemptID != want.attemptID ||
			attemptCount != want.attemptCount || lastError != "delivery-error" ||
			lease.Valid != want.lease || nextRetry.Valid != want.nextRetry || !sentAtMatches {
			t.Fatalf("%s delivery %d changed: status=%q decision=%q key=%q attempt_id=%q attempts=%d error=%q lease=%t next_retry=%t sent=%t sent_at=%s",
				fixture.engine, id, status, decision, key, attemptID, attemptCount, lastError, lease.Valid, nextRetry.Valid, sentAt.Valid, sentAt.Time.UTC().Format(time.RFC3339Nano))
		}
	}
}

type serviceMonitorRetirementPreservationSnapshot map[string][]string

func captureServiceMonitorRetirementPreservedData(t *testing.T, fixture migrationFixture, db *sql.DB) serviceMonitorRetirementPreservationSnapshot {
	t.Helper()
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)
	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "policies", query: `SELECT * FROM policies WHERE id = ?`, args: []any{1}},
		{name: "tasks", query: `SELECT * FROM tasks WHERE id = ?`, args: []any{500}},
		{name: "task_runs", query: `SELECT * FROM task_runs WHERE id = ?`, args: []any{501}},
		{name: "task_logs", query: `SELECT * FROM task_logs WHERE id = ?`, args: []any{502}},
		{name: "backup_repositories", query: `SELECT * FROM backup_repositories WHERE id = ?`, args: []any{repositoryID}},
		{name: "recovery_points", query: `SELECT * FROM recovery_points WHERE id = ?`, args: []any{pointID}},
		{name: "backup_completions", query: `SELECT * FROM backup_completions WHERE id = ?`, args: []any{601}},
		{name: "restore_drill_evidences", query: `SELECT * FROM restore_drill_evidences WHERE id = ?`, args: []any{503}},
		{name: "audit_logs", query: `SELECT * FROM audit_logs WHERE id = ?`, args: []any{504}},
		{name: "anomaly_events", query: `SELECT * FROM anomaly_events WHERE id = ?`, args: []any{203}},
		{name: "slo_definitions", query: `SELECT * FROM slo_definitions WHERE id = ?`, args: []any{2}},
	}
	snapshot := make(serviceMonitorRetirementPreservationSnapshot, len(queries))
	for _, spec := range queries {
		rows := snapshotServiceMonitorRetirementRows(t, fixture, db, spec.query, spec.args...)
		if len(rows) != 1 {
			t.Fatalf("%s retained %s row count=%d, want 1", fixture.engine, spec.name, len(rows))
		}
		snapshot[spec.name] = rows
	}
	return snapshot
}

func snapshotServiceMonitorRetirementRows(t *testing.T, fixture migrationFixture, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(fixture.bind(query), args...)
	if err != nil {
		t.Fatalf("query %s retained migration snapshot rows: %v", fixture.engine, err)
	}
	defer closeMigrationRows(t, rows)
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("read %s retained migration snapshot columns: %v", fixture.engine, err)
	}
	var values []string
	for rows.Next() {
		raw := make([]any, len(columns))
		dest := make([]any, len(columns))
		for index := range raw {
			dest[index] = &raw[index]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan %s retained migration snapshot row: %v", fixture.engine, err)
		}
		fields := make([]string, len(columns))
		for index, value := range raw {
			fields[index] = columns[index] + "=" + formatServiceMonitorRetirementSnapshotValue(value)
		}
		values = append(values, strings.Join(fields, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s retained migration snapshot rows: %v", fixture.engine, err)
	}
	return values
}

func formatServiceMonitorRetirementSnapshotValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "<null>"
	case []byte:
		return "bytes:" + string(value)
	case time.Time:
		return "time:" + value.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%T:%v", value, value)
	}
}

func assertServiceMonitorRetirementPreservedData(t *testing.T, fixture migrationFixture, before, after serviceMonitorRetirementPreservationSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s retained backup/task/recovery rows changed across service-monitor retirement: before=%v after=%v", fixture.engine, before, after)
	}
}
