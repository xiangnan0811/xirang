BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM task_runs
        WHERE trigger_type = 'drill'
          AND status IN ('pending', 'running', 'retrying')
    ) THEN
        RAISE EXCEPTION '000074 down rejected active restore drills';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_drill_durable_recovery_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS drill_durable_recovery_downgrade_admission();
DROP INDEX IF EXISTS idx_task_runs_active_drill;
DROP INDEX IF EXISTS idx_restore_drill_recovery_lease;
ALTER TABLE restore_drill_evidences
    DROP COLUMN IF EXISTS recovery_lease_until,
    DROP COLUMN IF EXISTS recovery_owner_id;

COMMIT;
