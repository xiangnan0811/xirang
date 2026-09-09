
-- Metadata admission normally rejects this downgrade before the migration
-- driver marks schema_migrations dirty. Keep an independent body guard for
-- direct/manual execution paths as well.
CREATE TEMP TABLE task_run_terminal_effects_000079_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO task_run_terminal_effects_000079_down_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM task_run_effects)
    OR EXISTS (
        SELECT 1
        FROM task_runs
        WHERE COALESCE(trigger_type, '') <> 'drill'
          AND status IN ('pending', 'running', 'retrying')
          AND execution_lease_until IS NOT NULL
          AND execution_lease_until > CURRENT_TIMESTAMP
    )
    THEN 0 ELSE 1 END;
DROP TABLE task_run_terminal_effects_000079_down_guard;

DROP TRIGGER IF EXISTS trg_task_run_terminal_effects_downgrade_admission;
DROP INDEX IF EXISTS idx_task_runs_downstream_once;
DROP INDEX IF EXISTS idx_task_run_effects_task_run;
DROP INDEX IF EXISTS idx_task_run_effects_ready;
DROP TABLE IF EXISTS task_run_effects;
DROP INDEX IF EXISTS idx_task_runs_execution_lease;
ALTER TABLE task_runs DROP COLUMN execution_lease_until;
ALTER TABLE task_runs DROP COLUMN execution_owner_id;
