-- 000091 retires service HTTP/TCP monitoring after fencing its historical alerts.
-- This migration is intentionally irreversible; the version-floor triggers also
-- protect golang-migrate's metadata write that precedes a down migration.
DROP TRIGGER IF EXISTS trg_service_monitor_retirement_downgrade_admission;
CREATE TRIGGER trg_service_monitor_retirement_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 91
BEGIN
    SELECT RAISE(ABORT, '000091 downgrade blocked: service-monitor retirement is irreversible');
END;

DROP TRIGGER IF EXISTS trg_service_monitor_retirement_downgrade_update_admission;
CREATE TRIGGER trg_service_monitor_retirement_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 91
BEGIN
    SELECT RAISE(ABORT, '000091 downgrade blocked: service-monitor retirement is irreversible');
END;

-- Materialize provenance before deleting the service-monitor tables. The
-- numeric-tail predicates are anchored: GLOB's first character class requires
-- a digit and the second condition rejects every non-digit thereafter.
CREATE TEMP TABLE service_monitor_retired_alert_ids (
    alert_id INTEGER PRIMARY KEY
);

INSERT OR IGNORE INTO service_monitor_retired_alert_ids(alert_id)
SELECT id
FROM alerts
WHERE error_code GLOB 'XR-SERVICE-DOWN-[0-9]*'
  AND error_code NOT GLOB 'XR-SERVICE-DOWN-[0-9]*[^0-9]*';

UPDATE alerts
SET status = CASE
        WHEN status IN ('open', 'acked') THEN 'resolved'
        ELSE status
    END,
    retryable = 0,
    delivery_decision = 'unknown',
    delivery_reason = 'feature_retired',
    delivery_decided_at = COALESCE(delivery_decided_at, CURRENT_TIMESTAMP)
WHERE id IN (SELECT alert_id FROM service_monitor_retired_alert_ids);

-- Provider-success history is immutable: only non-sent rows are fenced.
UPDATE alert_deliveries
SET decision = 'unknown',
    status = 'failed',
    lease_expires_at = NULL,
    next_retry_at = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE alert_id IN (SELECT alert_id FROM service_monitor_retired_alert_ids)
  AND status <> 'sent';

DROP TABLE IF EXISTS service_uptime_samples;
DROP TABLE IF EXISTS service_monitors;
DROP TABLE service_monitor_retired_alert_ids;
