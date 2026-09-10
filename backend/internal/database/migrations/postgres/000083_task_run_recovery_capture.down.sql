BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM task_runs
        WHERE COALESCE(backup_capture_layout, '') <> ''
           OR COALESCE(backup_capture_root, '') <> ''
           OR COALESCE(backup_capture_manifest, '') <> ''
           OR COALESCE(backup_generation_state, '') <> ''
           OR COALESCE(backup_source_run_id, 0) <> 0
    ) THEN
        RAISE EXCEPTION '000083 downgrade blocked: Rsync recovery capture evidence exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_task_runs_recovery_capture_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS task_runs_recovery_capture_downgrade_admission();
ALTER TABLE task_runs
    DROP COLUMN IF EXISTS backup_source_run_id,
    DROP COLUMN IF EXISTS backup_generation_state,
    DROP COLUMN IF EXISTS backup_capture_manifest,
    DROP COLUMN IF EXISTS backup_capture_root,
    DROP COLUMN IF EXISTS backup_capture_layout;

COMMIT;
