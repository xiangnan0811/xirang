-- Metadata admission normally rejects this downgrade before the migration
-- driver marks schema_migrations dirty. Keep an independent body guard for
-- direct/manual execution paths as well.
CREATE TEMP TABLE task_runs_000083_recovery_capture_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO task_runs_000083_recovery_capture_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM task_runs
    WHERE COALESCE(backup_capture_layout, '') <> ''
       OR COALESCE(backup_capture_root, '') <> ''
       OR COALESCE(backup_capture_manifest, '') <> ''
       OR COALESCE(backup_generation_state, '') <> ''
       OR COALESCE(backup_source_run_id, 0) <> 0
) THEN 0 ELSE 1 END;
DROP TABLE task_runs_000083_recovery_capture_down_guard;

DROP TRIGGER IF EXISTS trg_task_runs_recovery_capture_downgrade_admission;
ALTER TABLE task_runs DROP COLUMN backup_source_run_id;
ALTER TABLE task_runs DROP COLUMN backup_generation_state;
ALTER TABLE task_runs DROP COLUMN backup_capture_manifest;
ALTER TABLE task_runs DROP COLUMN backup_capture_root;
ALTER TABLE task_runs DROP COLUMN backup_capture_layout;
