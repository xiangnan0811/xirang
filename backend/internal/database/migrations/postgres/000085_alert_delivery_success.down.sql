BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM alert_deliveries
        WHERE sent_at IS NOT NULL
           OR COALESCE(decision, '') = 'unknown'
    ) THEN
        RAISE EXCEPTION '000085 downgrade blocked: alert delivery success or unknown identity evidence exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_alert_delivery_success_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS alert_delivery_success_downgrade_admission();
DROP INDEX IF EXISTS idx_task_runs_backup_source_run_id;
ALTER TABLE alert_deliveries DROP COLUMN IF EXISTS sent_at;

COMMIT;
