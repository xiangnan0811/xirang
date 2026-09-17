BEGIN;

-- 000085 records the provider-success time used by notification cooldowns.
-- Historical sent rows remain NULL because their completion time is unknown.
ALTER TABLE alert_deliveries
    ADD COLUMN IF NOT EXISTS sent_at TIMESTAMPTZ;

-- Recovery cleanup queries use the source run as a durable provenance key.
CREATE INDEX IF NOT EXISTS idx_task_runs_backup_source_run_id
    ON task_runs(backup_source_run_id);

-- Never erase provider-success evidence or an explicitly unknown historical
-- delivery identity on downgrade.
CREATE OR REPLACE FUNCTION alert_delivery_success_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 85 AND EXISTS (
        SELECT 1 FROM alert_deliveries
        WHERE sent_at IS NOT NULL
           OR COALESCE(decision, '') = 'unknown'
    ) THEN
        RAISE EXCEPTION '000085 downgrade blocked: alert delivery success or unknown identity evidence exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_alert_delivery_success_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_alert_delivery_success_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION alert_delivery_success_downgrade_admission();

COMMIT;
