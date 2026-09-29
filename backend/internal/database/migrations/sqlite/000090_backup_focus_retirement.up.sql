-- 000090 retires the node monitoring, node system-log and dashboard storage
-- after fencing their historical alert sources. This migration is intentionally
-- irreversible; the version-floor triggers also protect golang-migrate's
-- metadata write that precedes a down migration.
DROP TRIGGER IF EXISTS trg_backup_focus_retirement_downgrade_admission;
CREATE TRIGGER trg_backup_focus_retirement_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 90
BEGIN
    SELECT RAISE(ABORT, '000090 downgrade blocked: backup-focus retirement is irreversible');
END;

DROP TRIGGER IF EXISTS trg_backup_focus_retirement_downgrade_update_admission;
CREATE TRIGGER trg_backup_focus_retirement_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 90
BEGIN
    SELECT RAISE(ABORT, '000090 downgrade blocked: backup-focus retirement is irreversible');
END;

-- Materialize provenance before deleting anomaly events or clearing SLO links.
-- The numeric-tail predicates are anchored: GLOB's first character class
-- requires a digit and the second condition rejects every non-digit thereafter.
CREATE TEMP TABLE backup_focus_retired_alert_ids (
    alert_id INTEGER PRIMARY KEY
);

INSERT OR IGNORE INTO backup_focus_retired_alert_ids(alert_id)
SELECT id
FROM alerts
WHERE error_code = 'XR-NODE-DISK-FULL'
   OR (error_code GLOB 'XR-NODE-[0-9]*'
       AND error_code NOT GLOB 'XR-NODE-[0-9]*[^0-9]*')
   OR (error_code GLOB 'XR-ANOMALY-CPU-[0-9]*'
       AND error_code NOT GLOB 'XR-ANOMALY-CPU-[0-9]*[^0-9]*')
   OR (error_code GLOB 'XR-ANOMALY-MEM-[0-9]*'
       AND error_code NOT GLOB 'XR-ANOMALY-MEM-[0-9]*[^0-9]*')
   OR (error_code GLOB 'XR-ANOMALY-LOAD-[0-9]*'
       AND error_code NOT GLOB 'XR-ANOMALY-LOAD-[0-9]*[^0-9]*')
   OR (error_code GLOB 'XR-DISKFORECAST-[0-9]*'
       AND error_code NOT GLOB 'XR-DISKFORECAST-[0-9]*[^0-9]*');

-- An anomaly event records its alert id for deduplication even when it did not
-- newly raise an alert, so raised_alert must not participate in this predicate.
INSERT OR IGNORE INTO backup_focus_retired_alert_ids(alert_id)
SELECT alert_id
FROM anomaly_events
WHERE detector IN ('ewma', 'disk_forecast')
  AND alert_id IS NOT NULL;

INSERT OR IGNORE INTO backup_focus_retired_alert_ids(alert_id)
SELECT alerts.id
FROM alerts
JOIN slo_definitions ON slo_definitions.id = alerts.slo_id
WHERE slo_definitions.metric_type = 'availability';

UPDATE alerts
SET status = CASE
        WHEN status IN ('open', 'acked') THEN 'resolved'
        ELSE status
    END,
    retryable = 0,
    delivery_decision = 'unknown',
    delivery_reason = 'feature_retired',
    delivery_decided_at = COALESCE(delivery_decided_at, CURRENT_TIMESTAMP)
WHERE id IN (SELECT alert_id FROM backup_focus_retired_alert_ids);

-- Status 'sent' is a provider-success fact even for historical rows whose
-- sent_at is unknown. Only rows whose current status is not sent are fenced;
-- sent_at and all other attempt/error/key/history fields remain untouched.
UPDATE alert_deliveries
SET decision = 'unknown',
    status = 'failed',
    lease_expires_at = NULL,
    next_retry_at = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE alert_id IN (SELECT alert_id FROM backup_focus_retired_alert_ids)
  AND status <> 'sent';

-- Availability SLO links are retired before their definitions. Other SLO
-- definitions (including success_rate) and their alert history remain.
UPDATE alerts
SET slo_id = NULL
WHERE slo_id IN (
    SELECT id FROM slo_definitions WHERE metric_type = 'availability'
);
DELETE FROM slo_definitions
WHERE metric_type = 'availability';

DELETE FROM anomaly_events
WHERE detector IN ('ewma', 'disk_forecast');

DELETE FROM system_settings
WHERE key IN (
    'node.probe_interval',
    'node.probe_fail_threshold',
    'node.probe_concurrency',
    'logs.retention_days_default',
    'anomaly.enabled',
    'anomaly.ewma_alpha',
    'anomaly.ewma_sigma',
    'anomaly.ewma_window_hours',
    'anomaly.ewma_min_samples',
    'anomaly.disk_forecast_days',
    'anomaly.disk_forecast_min_history_hours',
    'metrics.remote_url',
    'metrics.remote_bearer_token'
);

-- Drop dependent children before dashboard parents. DROP TABLE also removes
-- each table's own indexes and preserves all unrelated schema objects.
DROP TABLE IF EXISTS node_metric_samples_daily;
DROP TABLE IF EXISTS node_metric_samples_hourly;
DROP TABLE IF EXISTS node_metric_samples;
DROP TABLE IF EXISTS node_log_cursors;
DROP TABLE IF EXISTS node_logs;
DROP TABLE IF EXISTS dashboard_panels;
DROP TABLE IF EXISTS dashboards;

-- SQLite's native DROP COLUMN keeps unrelated indexes, foreign keys and
-- triggers intact and avoids a hand-written table-rebuild copy.
ALTER TABLE nodes DROP COLUMN disk_used_gb;
ALTER TABLE nodes DROP COLUMN disk_total_gb;
ALTER TABLE nodes DROP COLUMN last_probe_at;
ALTER TABLE nodes DROP COLUMN consecutive_failures;
ALTER TABLE nodes DROP COLUMN log_paths;
ALTER TABLE nodes DROP COLUMN log_journalctl_enabled;
ALTER TABLE nodes DROP COLUMN log_retention_days;
ALTER TABLE reports DROP COLUMN disk_trend;
ALTER TABLE anomaly_events DROP COLUMN forecast_days;

DROP TABLE backup_focus_retired_alert_ids;
