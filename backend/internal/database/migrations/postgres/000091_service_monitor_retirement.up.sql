BEGIN;

-- 000091 retires service HTTP/TCP monitoring after fencing its historical
-- alerts. This migration is intentionally irreversible; the version-floor
-- trigger also protects golang-migrate's metadata write that precedes a down
-- migration.
CREATE OR REPLACE FUNCTION service_monitor_retirement_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 91 THEN
        RAISE EXCEPTION '000091 downgrade blocked: service-monitor retirement is irreversible';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_service_monitor_retirement_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_service_monitor_retirement_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW
EXECUTE FUNCTION service_monitor_retirement_downgrade_admission();

-- Materialize provenance before deleting the service-monitor tables. The
-- regular expression is anchored so only a numeric suffix is retired.
CREATE TEMP TABLE service_monitor_retired_alert_ids (
    alert_id BIGINT PRIMARY KEY
) ON COMMIT DROP;

INSERT INTO service_monitor_retired_alert_ids(alert_id)
SELECT id
FROM alerts
WHERE error_code ~ '^XR-SERVICE-DOWN-[0-9]+$'
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

COMMIT;
