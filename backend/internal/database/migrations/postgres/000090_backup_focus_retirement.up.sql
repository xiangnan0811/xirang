BEGIN;

-- 000090 retires the node monitoring, node system-log and dashboard storage
-- after fencing their historical alert sources. This migration is intentionally
-- irreversible; the version-floor trigger also protects golang-migrate's
-- metadata write that precedes a down migration.
CREATE OR REPLACE FUNCTION backup_focus_retirement_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 90 THEN
        RAISE EXCEPTION '000090 downgrade blocked: backup-focus retirement is irreversible';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_backup_focus_retirement_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_backup_focus_retirement_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW
EXECUTE FUNCTION backup_focus_retirement_downgrade_admission();

-- Materialize provenance before deleting anomaly events or clearing SLO links.
CREATE TEMP TABLE backup_focus_retired_alert_ids (
    alert_id BIGINT PRIMARY KEY
) ON COMMIT DROP;

INSERT INTO backup_focus_retired_alert_ids(alert_id)
SELECT id
FROM alerts
WHERE error_code = 'XR-NODE-DISK-FULL'
   OR error_code ~ '^XR-NODE-[0-9]+$'
   OR error_code ~ '^XR-ANOMALY-CPU-[0-9]+$'
   OR error_code ~ '^XR-ANOMALY-MEM-[0-9]+$'
   OR error_code ~ '^XR-ANOMALY-LOAD-[0-9]+$'
   OR error_code ~ '^XR-DISKFORECAST-[0-9]+$'
ON CONFLICT (alert_id) DO NOTHING;

-- An anomaly event records its alert id for deduplication even when it did not
-- newly raise an alert, so raised_alert must not participate in this predicate.
INSERT INTO backup_focus_retired_alert_ids(alert_id)
SELECT alert_id
FROM anomaly_events
WHERE detector IN ('ewma', 'disk_forecast')
  AND alert_id IS NOT NULL
ON CONFLICT (alert_id) DO NOTHING;

INSERT INTO backup_focus_retired_alert_ids(alert_id)
SELECT alerts.id
FROM alerts
JOIN slo_definitions ON slo_definitions.id = alerts.slo_id
WHERE slo_definitions.metric_type = 'availability'
ON CONFLICT (alert_id) DO NOTHING;

UPDATE alerts
SET status = CASE
        WHEN status IN ('open', 'acked') THEN 'resolved'
        ELSE status
    END,
    retryable = FALSE,
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

ALTER TABLE nodes
    DROP COLUMN IF EXISTS disk_used_gb,
    DROP COLUMN IF EXISTS disk_total_gb,
    DROP COLUMN IF EXISTS last_probe_at,
    DROP COLUMN IF EXISTS consecutive_failures,
    DROP COLUMN IF EXISTS log_paths,
    DROP COLUMN IF EXISTS log_journalctl_enabled,
    DROP COLUMN IF EXISTS log_retention_days;
ALTER TABLE reports DROP COLUMN IF EXISTS disk_trend;
ALTER TABLE anomaly_events DROP COLUMN IF EXISTS forecast_days;

DROP TABLE backup_focus_retired_alert_ids;

COMMIT;
