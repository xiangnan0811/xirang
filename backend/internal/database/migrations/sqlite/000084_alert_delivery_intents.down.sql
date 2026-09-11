CREATE TEMP TABLE alert_delivery_intents_000084_downgrade_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO alert_delivery_intents_000084_downgrade_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM alerts
    WHERE COALESCE(delivery_decision, '') <> ''
       OR delivery_decided_at IS NOT NULL
) OR EXISTS (
    SELECT 1 FROM alert_deliveries
    WHERE COALESCE(delivery_key, '') <> ''
       OR COALESCE(attempt_id, '') <> ''
       OR lease_expires_at IS NOT NULL
) THEN 0 ELSE 1 END;
DROP TABLE alert_delivery_intents_000084_downgrade_guard;

DROP TRIGGER IF EXISTS trg_alert_delivery_intents_downgrade_admission;
DROP INDEX IF EXISTS idx_alert_deliveries_delivery_key;
DROP INDEX IF EXISTS idx_alert_deliveries_claim;

ALTER TABLE alert_deliveries DROP COLUMN updated_at;
ALTER TABLE alert_deliveries DROP COLUMN lease_expires_at;
ALTER TABLE alert_deliveries DROP COLUMN attempt_id;
ALTER TABLE alert_deliveries DROP COLUMN delivery_key;
ALTER TABLE alert_deliveries DROP COLUMN decision;
ALTER TABLE alerts DROP COLUMN delivery_decided_at;
ALTER TABLE alerts DROP COLUMN delivery_reason;
ALTER TABLE alerts DROP COLUMN delivery_decision;
