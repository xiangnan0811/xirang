-- Metadata admission normally rejects this downgrade before the migration
-- driver marks schema_migrations dirty. Keep an independent body guard for
-- direct/manual execution paths as well.
CREATE TEMP TABLE task_runs_000082_cron_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO task_runs_000082_cron_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM task_runs WHERE cron_scheduled_at IS NOT NULL
       OR COALESCE(backup_config_fingerprint, '') <> ''
) THEN 0 ELSE 1 END;
DROP TABLE task_runs_000082_cron_down_guard;

DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_downgrade_admission;
DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_immutable;
DROP INDEX IF EXISTS idx_task_runs_cron_occurrence;
ALTER TABLE task_runs DROP COLUMN backup_config_fingerprint;
ALTER TABLE task_runs DROP COLUMN cron_scheduled_at;
