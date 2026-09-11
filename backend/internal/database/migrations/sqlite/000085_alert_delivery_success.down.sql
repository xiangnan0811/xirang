CREATE TEMP TABLE alert_delivery_success_000085_downgrade_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO alert_delivery_success_000085_downgrade_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM alert_deliveries
    WHERE sent_at IS NOT NULL
       OR COALESCE(decision, '') = 'unknown'
) THEN 0 ELSE 1 END;
DROP TABLE alert_delivery_success_000085_downgrade_guard;

DROP TRIGGER IF EXISTS trg_alert_delivery_success_downgrade_admission;
DROP INDEX IF EXISTS idx_task_runs_backup_source_run_id;
ALTER TABLE alert_deliveries DROP COLUMN sent_at;
