BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM task_runs
        WHERE cron_scheduled_at IS NOT NULL
           OR COALESCE(backup_config_fingerprint, '') <> ''
    ) THEN
        RAISE EXCEPTION '000082 downgrade blocked: TaskRun cron occurrence or backup provenance exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS task_runs_cron_provenance_downgrade_admission();
DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_immutable ON task_runs;
DROP FUNCTION IF EXISTS task_runs_cron_provenance_immutable_guard();
DROP INDEX IF EXISTS idx_task_runs_cron_occurrence;
ALTER TABLE task_runs
    DROP COLUMN IF EXISTS backup_config_fingerprint,
    DROP COLUMN IF EXISTS cron_scheduled_at;

COMMIT;
