DROP TABLE IF EXISTS drill_durable_recovery_000074_down_guard;
CREATE TEMP TABLE drill_durable_recovery_000074_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO drill_durable_recovery_000074_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM task_runs
    WHERE trigger_type = 'drill'
      AND status IN ('pending', 'running', 'retrying')
) THEN 0 ELSE 1 END;
DROP TABLE drill_durable_recovery_000074_down_guard;

DROP TRIGGER IF EXISTS trg_drill_durable_recovery_downgrade_admission;
DROP INDEX IF EXISTS idx_task_runs_active_drill;
DROP INDEX IF EXISTS idx_restore_drill_recovery_lease;
ALTER TABLE restore_drill_evidences DROP COLUMN recovery_lease_until;
ALTER TABLE restore_drill_evidences DROP COLUMN recovery_owner_id;
