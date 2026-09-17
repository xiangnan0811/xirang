-- 000084 makes the alert decision and each channel delivery intent durable.
-- NULL alert decisions are historical/unknown; new code writes an explicit
-- pending decision before any network call.
ALTER TABLE alerts ADD COLUMN delivery_decision VARCHAR(32);
ALTER TABLE alerts ADD COLUMN delivery_reason VARCHAR(64);
ALTER TABLE alerts ADD COLUMN delivery_decided_at DATETIME;

-- Existing rows receive the neutral deliver decision for backwards
-- compatibility. The alert-level decision remains NULL for historical alerts.
ALTER TABLE alert_deliveries ADD COLUMN decision VARCHAR(16) NOT NULL DEFAULT 'deliver';
ALTER TABLE alert_deliveries ADD COLUMN delivery_key VARCHAR(96);
ALTER TABLE alert_deliveries ADD COLUMN attempt_id VARCHAR(64);
ALTER TABLE alert_deliveries ADD COLUMN lease_expires_at DATETIME;
ALTER TABLE alert_deliveries ADD COLUMN updated_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';

-- One logical alert/channel intent. Empty keys are legacy rows and are not
-- admitted to the uniqueness constraint until replay adopts them.
CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_deliveries_delivery_key
    ON alert_deliveries(delivery_key)
    WHERE delivery_key IS NOT NULL AND delivery_key <> '';
CREATE INDEX IF NOT EXISTS idx_alert_deliveries_claim
    ON alert_deliveries(status, next_retry_at, lease_expires_at);

-- Never erase a recorded decision or in-flight delivery lease on downgrade.
DROP TRIGGER IF EXISTS trg_alert_delivery_intents_downgrade_admission;
CREATE TRIGGER trg_alert_delivery_intents_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 84
 AND (
    EXISTS (
        SELECT 1 FROM alerts
        WHERE COALESCE(delivery_decision, '') <> ''
           OR delivery_decided_at IS NOT NULL
    )
    OR EXISTS (
        SELECT 1 FROM alert_deliveries
        WHERE COALESCE(delivery_key, '') <> ''
           OR COALESCE(attempt_id, '') <> ''
           OR lease_expires_at IS NOT NULL
    )
 )
BEGIN
    SELECT RAISE(ABORT, '000084 downgrade blocked: alert delivery decision or lease evidence exists');
END;
