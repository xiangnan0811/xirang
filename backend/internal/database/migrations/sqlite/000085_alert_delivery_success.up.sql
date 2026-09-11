-- 000085 records the provider-success time used by notification cooldowns.
-- Historical sent rows remain NULL because their completion time is unknown.
ALTER TABLE alert_deliveries ADD COLUMN sent_at DATETIME;

-- Recovery cleanup queries use the source run as a durable provenance key.
CREATE INDEX IF NOT EXISTS idx_task_runs_backup_source_run_id
    ON task_runs(backup_source_run_id);

-- Never erase provider-success evidence or an explicitly unknown historical
-- delivery identity on downgrade.
DROP TRIGGER IF EXISTS trg_alert_delivery_success_downgrade_admission;
CREATE TRIGGER trg_alert_delivery_success_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 85
 AND (
    EXISTS (
        SELECT 1 FROM alert_deliveries
        WHERE sent_at IS NOT NULL
           OR COALESCE(decision, '') = 'unknown'
    )
 )
BEGIN
    SELECT RAISE(ABORT, '000085 downgrade blocked: alert delivery success or unknown identity evidence exists');
END;
