BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM task_run_effects) THEN
        RAISE EXCEPTION '000079 downgrade blocked: task run effects exist';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM task_runs
        WHERE COALESCE(trigger_type, '') <> 'drill'
          AND status IN ('pending', 'running', 'retrying')
          AND execution_lease_until IS NOT NULL
          AND execution_lease_until > NOW()
    ) THEN
        RAISE EXCEPTION '000079 downgrade blocked: live ordinary execution lease exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_task_run_terminal_effects_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS task_run_terminal_effects_downgrade_admission();
DROP INDEX IF EXISTS idx_task_runs_downstream_once;
DROP INDEX IF EXISTS idx_task_run_effects_task_run;
DROP INDEX IF EXISTS idx_task_run_effects_ready;
DROP TABLE IF EXISTS task_run_effects;
DROP INDEX IF EXISTS idx_task_runs_execution_lease;
ALTER TABLE task_runs
    DROP COLUMN execution_lease_until,
    DROP COLUMN execution_owner_id;
COMMIT;
