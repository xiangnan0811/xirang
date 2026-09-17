BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM alerts
        WHERE COALESCE(delivery_decision, '') <> ''
           OR delivery_decided_at IS NOT NULL
    ) OR EXISTS (
        SELECT 1 FROM alert_deliveries
        WHERE COALESCE(delivery_key, '') <> ''
           OR COALESCE(attempt_id, '') <> ''
           OR lease_expires_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION '000084 downgrade blocked: alert delivery decision or lease evidence exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_alert_delivery_intents_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS alert_delivery_intents_downgrade_admission();
DROP INDEX IF EXISTS idx_alert_deliveries_delivery_key;
DROP INDEX IF EXISTS idx_alert_deliveries_claim;

ALTER TABLE alert_deliveries
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS attempt_id,
    DROP COLUMN IF EXISTS delivery_key,
    DROP COLUMN IF EXISTS decision;
ALTER TABLE alerts
    DROP COLUMN IF EXISTS delivery_decided_at,
    DROP COLUMN IF EXISTS delivery_reason,
    DROP COLUMN IF EXISTS delivery_decision;

COMMIT;
