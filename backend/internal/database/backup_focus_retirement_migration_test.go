package database

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

const backupFocusRetirementMigrationVersion uint = 90

func TestBackupFocusRetirementMigrationSQLite(t *testing.T) {
	testBackupFocusRetirementMigration(t, newSQLiteMigrationFixture(t))
}

func TestBackupFocusRetirementMigrationPostgres(t *testing.T) {
	testBackupFocusRetirementMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testBackupFocusRetirementMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("mixed data is retired and retained contracts survive", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupFocusRetirementMigrationVersion-1)
		seedBackupFocusRetirementMixedData(t, fixture, db)

		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply 000090 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, backupFocusRetirementMigrationVersion)
		assertBackupFocusRetirementSchema(t, fixture, db)
		assertBackupFocusRetirementData(t, fixture, db)

		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("000090 Steps(-1) on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, backupFocusRetirementMigrationVersion)
		assertBackupFocusRetirementData(t, fixture, db)
	})

	t.Run("empty schema has an irreversible clean head", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupFocusRetirementMigrationVersion-1)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply empty 000090 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, backupFocusRetirementMigrationVersion)
		assertBackupFocusRetirementRetiredSchemaAbsent(t, fixture, db)
		assertBackupFocusRetirementAdmissionTriggers(t, fixture, db)

		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("empty 000090 Steps(-1) on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, backupFocusRetirementMigrationVersion)
		assertBackupFocusRetirementRetiredSchemaAbsent(t, fixture, db)
	})
}

func seedBackupFocusRetirementMixedData(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)
	future := now.Add(time.Hour)
	backupRepositoryID := strings.Repeat("a", 32)

	fixture.mustExec(t, db, `INSERT INTO users
		(id, username, password_hash, role, created_at, updated_at)
		VALUES (1, 'retirement-user', 'hash', 'admin', ?, ?)`, now, now)
	fixture.mustExec(t, db, `INSERT INTO ssh_keys
		(id, name, username, private_key, fingerprint, created_at, updated_at)
		VALUES (2, 'retirement-key', 'root', 'private-key', 'fingerprint', ?, ?)`, now, now)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, ssh_key_id, status, connection_latency, last_seen_at, last_backup_at, created_at, updated_at)
		VALUES (1, 'retirement-node', '127.0.0.1', 'root', 2, 'online', 17, ?, ?, ?, ?)`, now, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO policies
		(id, name, source_path, target_path, cron_spec, created_at, updated_at)
		VALUES (1, 'retirement-policy', '/source', '/target', '* * * * *', ?, ?)`, now, now)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (500, 'retained-task', 1, 'local', 'idle', ?, ?)`, now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, finished_at, created_at, updated_at)
		VALUES (501, 500, 1, 'manual', 'success', ?, ?, ?)`, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO task_logs
		(id, task_id, level, message, created_at)
		VALUES (502, 500, 'info', 'retained task log', ?)`, now)
	fixture.mustExec(t, db, `INSERT INTO restore_drill_evidences
		(id, policy_id, task_id, task_run_id, sandbox_node_id, sandbox_path, status, started_at, created_at, updated_at)
		VALUES (503, 1, 500, 501, 1, '/tmp/retained-drill', 'succeeded', ?, ?, ?)`, now, now, now)
	fixture.insertRepository(t, db, backupRepositoryID, "restic", now)
	fixture.mustExec(t, db, `INSERT INTO audit_logs
		(id, user_id, username, role, method, path, status_code, created_at)
		VALUES (504, 1, 'retirement-user', 'admin', 'GET', '/api/v1/retained', 200, ?)`, now)

	fixture.mustExec(t, db, `INSERT INTO report_configs
		(id, name, created_at, updated_at)
		VALUES (1, 'retained-report', ?, ?)`, now, now)
	fixture.mustExec(t, db, `INSERT INTO reports
		(id, config_id, period_start, period_end, total_runs, success_runs, failed_runs, success_rate, avg_duration_ms, top_failures, disk_trend, generated_at, created_at, updated_at)
		VALUES (505, 1, ?, ?, 3, 2, 1, 0.666, 42, '[]', '[{"disk":1}]', ?, ?, ?)`, now, now, now, now, now)

	for _, key := range []string{
		"node.probe_interval",
		"node.probe_fail_threshold",
		"node.probe_concurrency",
		"logs.retention_days_default",
		"anomaly.enabled",
		"anomaly.ewma_alpha",
		"anomaly.ewma_sigma",
		"anomaly.ewma_window_hours",
		"anomaly.ewma_min_samples",
		"anomaly.disk_forecast_days",
		"anomaly.disk_forecast_min_history_hours",
		"metrics.remote_url",
		"metrics.remote_bearer_token",
		"anomaly.alerts_enabled",
		"backup_assets.enabled",
	} {
		fixture.mustExec(t, db, `INSERT INTO system_settings(key, value, updated_at) VALUES (?, 'retained-test-value', ?)`, key, now)
	}

	fixture.mustExec(t, db, `INSERT INTO slo_definitions
		(id, name, metric_type, threshold, created_by, created_at, updated_at)
		VALUES (1, 'availability-retired', 'availability', 0.99, 1, ?, ?)`, now, now)
	fixture.mustExec(t, db, `INSERT INTO slo_definitions
		(id, name, metric_type, threshold, created_by, created_at, updated_at)
		VALUES (2, 'success-retained', 'success_rate', 0.99, 1, ?, ?)`, now, now)

	insertAlert := func(id int, status, code string, retryable bool, sloID any) {
		fixture.mustExec(t, db, `INSERT INTO alerts
			(id, node_id, node_name, severity, status, error_code, message, retryable, slo_id, created_at, updated_at)
			VALUES (?, 1, 'retirement-node', 'warning', ?, ?, 'retirement alert', ?, ?, ?, ?)`,
			id, status, code, retryable, sloID, now, now)
	}
	insertAlert(101, "open", "XR-NODE-42", true, nil)
	insertAlert(102, "acked", "XR-NODE-DISK-FULL", true, nil)
	insertAlert(103, "resolved", "XR-ANOMALY-CPU-7", true, nil)
	insertAlert(104, "open", "XR-ANOMALY-MEM-8", true, nil)
	insertAlert(105, "open", "XR-ANOMALY-LOAD-9", true, nil)
	insertAlert(106, "open", "XR-DISKFORECAST-10", true, nil)
	insertAlert(107, "open", "XR-OTHER-EVENT", true, nil)
	insertAlert(108, "open", "XR-SLO-1", true, 1)
	insertAlert(109, "open", "XR-SLO-2", true, 2)
	insertAlert(110, "open", "XR-NODE-EXPIRY-3", true, nil)
	insertAlert(111, "open", "XR-NODE-5abc", true, nil)
	insertAlert(112, "open", "XR-OTHER-RETAINED", true, nil)

	fixture.mustExec(t, db, `INSERT INTO anomaly_events
		(id, node_id, detector, metric, severity, observed_value, baseline_value, sigma, forecast_days, alert_id, raised_alert, details, fired_at)
		VALUES (201, 1, 'ewma', 'cpu', 'warning', 90, 20, 3.2, 7, 107, ?, '{}', ?)`, false, now)
	fixture.mustExec(t, db, `INSERT INTO anomaly_events
		(id, node_id, detector, metric, severity, observed_value, baseline_value, sigma, forecast_days, alert_id, raised_alert, details, fired_at)
		VALUES (202, 1, 'disk_forecast', 'disk', 'warning', 90, 20, 4.2, 14, 106, ?, '{}', ?)`, true, now)
	fixture.mustExec(t, db, `INSERT INTO anomaly_events
		(id, node_id, detector, metric, severity, observed_value, baseline_value, sigma, forecast_days, alert_id, raised_alert, details, fired_at)
		VALUES (203, 1, 'snapshot_diff', 'backup', 'warning', 1, 1, 1.2, NULL, 112, ?, '{}', ?)`, false, now)

	insertDelivery := func(id, alertID int, status, decision, key, attemptID string, attemptCount int, lease, nextRetry, sentAt any, lastError string) {
		fixture.mustExec(t, db, `INSERT INTO alert_deliveries
			(id, alert_id, integration_id, status, decision, delivery_key, attempt_id, attempt_count, lease_expires_at, next_retry_at, last_error, sent_at, created_at, updated_at)
			VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, alertID, status, decision, key, attemptID, attemptCount, lease, nextRetry, lastError, sentAt, now, now)
	}
	insertDelivery(301, 101, "pending", "deliver", "retired-key-301", "retired-attempt-301", 4, future, future, nil, "old-pending-error")
	insertDelivery(302, 102, "sent", "deliver", "sent-key-302", "sent-attempt-302", 2, nil, nil, now, "")
	insertDelivery(303, 107, "sending", "deliver", "retired-key-303", "retired-attempt-303", 7, future, nil, nil, "in-flight-error")
	insertDelivery(304, 108, "retrying", "deliver", "retired-key-304", "retired-attempt-304", 5, future, future, nil, "retry-error")
	insertDelivery(305, 109, "retrying", "deliver", "retained-key-305", "retained-attempt-305", 6, future, future, nil, "retained-error")
	insertDelivery(306, 110, "failed", "deliver", "expiry-key-306", "expiry-attempt-306", 1, nil, future, nil, "expiry-error")
	insertDelivery(307, 112, "sent", "deliver", "historical-sent-key-307", "historical-sent-attempt-307", 3, nil, nil, nil, "")
	insertDelivery(308, 101, "failed", "deliver", "historical-success-key-308", "historical-success-attempt-308", 8, nil, future, now, "historical-success-error")

	fixture.mustExec(t, db, `INSERT INTO node_metric_samples(id, node_id, sampled_at) VALUES (401, 1, ?)`, now)
	fixture.mustExec(t, db, `INSERT INTO node_metric_samples_hourly(id, node_id, bucket_start) VALUES (402, 1, ?)`, now)
	fixture.mustExec(t, db, `INSERT INTO node_metric_samples_daily(id, node_id, bucket_start) VALUES (403, 1, ?)`, now)
	fixture.mustExec(t, db, `INSERT INTO node_logs(id, node_id, source, path, timestamp, message) VALUES (404, 1, 'journal', '/var/log/messages', ?, 'retired log')`, now)
	fixture.mustExec(t, db, `INSERT INTO node_log_cursors(id, node_id, source, path) VALUES (405, 1, 'journal', '/var/log/messages')`)
	fixture.mustExec(t, db, `INSERT INTO dashboards(id, owner_id, name) VALUES (406, 1, 'retired-dashboard')`)
	fixture.mustExec(t, db, `INSERT INTO dashboard_panels(id, dashboard_id, title, chart_type, metric, aggregation) VALUES (407, 406, 'retired-panel', 'line', 'cpu', 'avg')`)
}

func assertBackupFocusRetirementSchema(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	assertBackupFocusRetirementRetiredSchemaAbsent(t, fixture, db)
	for _, table := range []string{
		"users", "nodes", "alerts", "alert_deliveries", "anomaly_events", "slo_definitions",
		"tasks", "task_runs", "task_logs", "restore_drill_evidences", "backup_repositories",
		"audit_logs", "report_configs", "reports", "system_settings",
	} {
		if !databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s retained table %s is missing", fixture.engine, table)
		}
	}
	for _, column := range []struct {
		table  string
		column string
	}{
		{"nodes", "status"},
		{"nodes", "connection_latency"},
		{"nodes", "last_seen_at"},
		{"nodes", "last_backup_at"},
		{"reports", "success_rate"},
		{"reports", "avg_duration_ms"},
		{"anomaly_events", "sigma"},
	} {
		if !databaseColumnExists(t, db, fixture.engine, column.table, column.column) {
			t.Fatalf("%s retained column %s.%s is missing", fixture.engine, column.table, column.column)
		}
	}
	for _, index := range []string{
		"idx_nodes_name",
		"idx_nodes_ssh_key_id",
		"idx_reports_config_id",
		"idx_reports_period_start",
		"idx_anomaly_events_node_fired",
		"idx_anomaly_events_detector_fired",
		"idx_alert_deliveries_retry",
		"idx_alert_deliveries_claim",
		"idx_restore_drill_evidences_task_run",
	} {
		if definition := fixture.indexDefinition(t, db, index); definition == "" {
			t.Fatalf("%s retained index %s is missing", fixture.engine, index)
		}
	}
	assertBackupFocusRetirementAdmissionTriggers(t, fixture, db)
	for _, trigger := range []string{
		"trg_backup_asset_recovery_downgrade_admission",
		"trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission",
	} {
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", trigger) {
			t.Fatalf("%s retained migration trigger %s is missing", fixture.engine, trigger)
		}
	}

	now := time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)
	if _, err := db.Exec(fixture.bind(`INSERT INTO nodes
		(id, name, host, username) VALUES (999, 'retirement-node', '192.0.2.99', 'root')`)); err == nil {
		t.Fatalf("%s retained unique node-name constraint accepted a duplicate", fixture.engine)
	}
	if _, err := db.Exec(fixture.bind(`INSERT INTO reports
		(config_id, period_start, period_end) VALUES (999, ?, ?)`), now, now); err == nil {
		t.Fatalf("%s retained report-config foreign key accepted an orphan", fixture.engine)
	}
	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyCheck(t, db)
	}
}

func assertBackupFocusRetirementRetiredSchemaAbsent(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"node_metric_samples", "node_metric_samples_hourly", "node_metric_samples_daily",
		"node_logs", "node_log_cursors", "dashboard_panels", "dashboards",
	} {
		if databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s retired table %s remains", fixture.engine, table)
		}
	}
	for _, column := range []struct {
		table  string
		column string
	}{
		{"nodes", "disk_used_gb"},
		{"nodes", "disk_total_gb"},
		{"nodes", "last_probe_at"},
		{"nodes", "consecutive_failures"},
		{"nodes", "log_paths"},
		{"nodes", "log_journalctl_enabled"},
		{"nodes", "log_retention_days"},
		{"reports", "disk_trend"},
		{"anomaly_events", "forecast_days"},
	} {
		if databaseColumnExists(t, db, fixture.engine, column.table, column.column) {
			t.Fatalf("%s retired column %s.%s remains", fixture.engine, column.table, column.column)
		}
	}
}

func assertBackupFocusRetirementAdmissionTriggers(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	if !fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_backup_focus_retirement_downgrade_admission") {
		t.Fatalf("%s retirement insert admission trigger is missing", fixture.engine)
	}
	if fixture.engine == "sqlite" {
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_backup_focus_retirement_downgrade_update_admission") {
			t.Fatalf("SQLite retirement update admission trigger is missing")
		}
	} else {
		triggerDefinition := strings.ToLower(fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_focus_retirement_downgrade_admission"))
		if !strings.Contains(triggerDefinition, "insert or update") {
			t.Fatalf("PostgreSQL retirement admission trigger does not cover INSERT OR UPDATE: %s", triggerDefinition)
		}
		functionDefinition := strings.ToLower(fixture.recoveryFunctionDefinition(t, db, "backup_focus_retirement_downgrade_admission"))
		if !strings.Contains(functionDefinition, "new.version < 90") {
			t.Fatalf("PostgreSQL retirement admission function is not an unconditional version-floor guard: %s", functionDefinition)
		}
	}
}

func assertBackupFocusRetirementData(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for _, id := range []int{101, 102, 103, 104, 105, 106, 107, 108} {
		assertRetiredAlert(t, fixture, db, id)
	}
	for _, id := range []int{109, 110, 111, 112} {
		var status, decision, reason string
		var retryable bool
		if err := db.QueryRow(fixture.bind(`SELECT status, retryable, COALESCE(delivery_decision, ''), COALESCE(delivery_reason, '') FROM alerts WHERE id = ?`), id).
			Scan(&status, &retryable, &decision, &reason); err != nil {
			t.Fatalf("read retained alert %d on %s: %v", id, fixture.engine, err)
		}
		if status != "open" || !retryable || decision != "" || reason != "" {
			t.Fatalf("%s retained alert %d changed: status=%q retryable=%t decision=%q reason=%q", fixture.engine, id, status, retryable, decision, reason)
		}
	}

	var sloCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM slo_definitions WHERE metric_type = 'availability'`)).Scan(&sloCount); err != nil {
		t.Fatalf("count availability SLOs on %s: %v", fixture.engine, err)
	}
	if sloCount != 0 {
		t.Fatalf("%s availability SLO count=%d, want 0", fixture.engine, sloCount)
	}
	var successSLOCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM slo_definitions WHERE id = 2 AND metric_type = 'success_rate'`)).Scan(&successSLOCount); err != nil {
		t.Fatalf("count success-rate SLO on %s: %v", fixture.engine, err)
	}
	if successSLOCount != 1 {
		t.Fatalf("%s success-rate SLO count=%d, want 1", fixture.engine, successSLOCount)
	}

	var anomalyCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM anomaly_events WHERE detector IN ('ewma', 'disk_forecast')`)).Scan(&anomalyCount); err != nil {
		t.Fatalf("count retired anomaly events on %s: %v", fixture.engine, err)
	}
	if anomalyCount != 0 {
		t.Fatalf("%s retired anomaly event count=%d, want 0", fixture.engine, anomalyCount)
	}
	var snapshotSigma float64
	if err := db.QueryRow(fixture.bind(`SELECT sigma FROM anomaly_events WHERE detector = 'snapshot_diff' AND id = 203`)).Scan(&snapshotSigma); err != nil {
		t.Fatalf("read retained snapshot-diff event on %s: %v", fixture.engine, err)
	}
	if snapshotSigma != 1.2 {
		t.Fatalf("%s retained snapshot-diff sigma=%v, want 1.2", fixture.engine, snapshotSigma)
	}

	for _, key := range []string{
		"node.probe_interval",
		"node.probe_fail_threshold",
		"node.probe_concurrency",
		"logs.retention_days_default",
		"anomaly.enabled",
		"anomaly.ewma_alpha",
		"anomaly.ewma_sigma",
		"anomaly.ewma_window_hours",
		"anomaly.ewma_min_samples",
		"anomaly.disk_forecast_days",
		"anomaly.disk_forecast_min_history_hours",
		"metrics.remote_url",
		"metrics.remote_bearer_token",
	} {
		assertRetirementCount(t, fixture, db, `SELECT COUNT(*) FROM system_settings WHERE key = ?`, 0, key)
	}
	for _, key := range []string{"anomaly.alerts_enabled", "backup_assets.enabled"} {
		assertRetirementCount(t, fixture, db, `SELECT COUNT(*) FROM system_settings WHERE key = ?`, 1, key)
	}

	for _, retained := range []struct {
		query string
		args  []any
	}{
		{`SELECT COUNT(*) FROM users WHERE id = ?`, []any{1}},
		{`SELECT COUNT(*) FROM ssh_keys WHERE id = ?`, []any{2}},
		{`SELECT COUNT(*) FROM tasks WHERE id = ?`, []any{500}},
		{`SELECT COUNT(*) FROM task_runs WHERE id = ?`, []any{501}},
		{`SELECT COUNT(*) FROM task_logs WHERE id = ?`, []any{502}},
		{`SELECT COUNT(*) FROM restore_drill_evidences WHERE id = ?`, []any{503}},
		{`SELECT COUNT(*) FROM backup_repositories WHERE id = ?`, []any{strings.Repeat("a", 32)}},
		{`SELECT COUNT(*) FROM audit_logs WHERE id = ?`, []any{504}},
		{`SELECT COUNT(*) FROM reports WHERE id = ?`, []any{505}},
	} {
		assertRetirementCount(t, fixture, db, retained.query, 1, retained.args...)
	}

	assertRetirementDelivery(t, fixture, db, 301, "failed", "unknown", "retired-key-301", "retired-attempt-301", 4, "old-pending-error", false, false, false)
	assertRetirementDelivery(t, fixture, db, 303, "failed", "unknown", "retired-key-303", "retired-attempt-303", 7, "in-flight-error", false, false, false)
	assertRetirementDelivery(t, fixture, db, 304, "failed", "unknown", "retired-key-304", "retired-attempt-304", 5, "retry-error", false, false, false)
	assertRetirementDelivery(t, fixture, db, 308, "failed", "unknown", "historical-success-key-308", "historical-success-attempt-308", 8, "historical-success-error", false, false, true)
	assertRetirementDelivery(t, fixture, db, 302, "sent", "deliver", "sent-key-302", "sent-attempt-302", 2, "", false, false, true)
	assertRetirementDelivery(t, fixture, db, 307, "sent", "deliver", "historical-sent-key-307", "historical-sent-attempt-307", 3, "", false, false, false)
	assertRetirementDelivery(t, fixture, db, 305, "retrying", "deliver", "retained-key-305", "retained-attempt-305", 6, "retained-error", true, true, false)
	assertRetirementDelivery(t, fixture, db, 306, "failed", "deliver", "expiry-key-306", "expiry-attempt-306", 1, "expiry-error", false, true, false)
}

func assertRetiredAlert(t *testing.T, fixture migrationFixture, db *sql.DB, id int) {
	t.Helper()
	var status, decision, reason string
	var retryable bool
	var sloID sql.NullInt64
	var decidedAt sql.NullTime
	if err := db.QueryRow(fixture.bind(`SELECT status, retryable, COALESCE(delivery_decision, ''), COALESCE(delivery_reason, ''), slo_id, delivery_decided_at FROM alerts WHERE id = ?`), id).
		Scan(&status, &retryable, &decision, &reason, &sloID, &decidedAt); err != nil {
		t.Fatalf("read retired alert %d on %s: %v", id, fixture.engine, err)
	}
	if status != "resolved" || retryable || decision != "unknown" || reason != "feature_retired" || !decidedAt.Valid {
		t.Fatalf("%s retired alert %d has status=%q retryable=%t decision=%q reason=%q decided_at_valid=%t", fixture.engine, id, status, retryable, decision, reason, decidedAt.Valid)
	}
	if id == 108 && sloID.Valid {
		t.Fatalf("%s availability-linked alert %d retained slo_id=%d", fixture.engine, id, sloID.Int64)
	}
}

func assertRetirementDelivery(t *testing.T, fixture migrationFixture, db *sql.DB, id int, wantStatus, wantDecision, wantKey, wantAttemptID string, wantAttemptCount int, wantLastError string, wantLease, wantNextRetry, wantSent bool) {
	t.Helper()
	var status, decision, key, attemptID, lastError string
	var attemptCount int
	var lease, nextRetry, sentAt sql.NullTime
	if err := db.QueryRow(fixture.bind(`SELECT status, decision, delivery_key, attempt_id, attempt_count, last_error, lease_expires_at, next_retry_at, sent_at FROM alert_deliveries WHERE id = ?`), id).
		Scan(&status, &decision, &key, &attemptID, &attemptCount, &lastError, &lease, &nextRetry, &sentAt); err != nil {
		t.Fatalf("read delivery %d on %s: %v", id, fixture.engine, err)
	}
	if status != wantStatus || decision != wantDecision || key != wantKey || attemptID != wantAttemptID || attemptCount != wantAttemptCount || lastError != wantLastError || lease.Valid != wantLease || nextRetry.Valid != wantNextRetry || sentAt.Valid != wantSent {
		t.Fatalf("%s delivery %d changed: status=%q decision=%q key=%q attempt_id=%q attempts=%d error=%q lease=%t next_retry=%t sent=%t", fixture.engine, id, status, decision, key, attemptID, attemptCount, lastError, lease.Valid, nextRetry.Valid, sentAt.Valid)
	}
}

func assertRetirementCount(t *testing.T, fixture migrationFixture, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(fixture.bind(query), args...).Scan(&got); err != nil {
		t.Fatalf("query retained row count on %s: %v", fixture.engine, err)
	}
	if got != want {
		t.Fatalf("%s retained row count=%d, want %d", fixture.engine, got, want)
	}
}
