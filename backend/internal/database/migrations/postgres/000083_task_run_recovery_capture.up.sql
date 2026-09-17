BEGIN;

-- 000083 records the logical Rsync capture root, source-side manifest, and
-- mutable-generation state. Historical rows remain unknown and are never
-- guessed into a restore authority.
ALTER TABLE task_runs
    ADD COLUMN backup_capture_layout VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN backup_capture_root VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN backup_capture_manifest TEXT NOT NULL DEFAULT '',
    ADD COLUMN backup_generation_state VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN backup_source_run_id BIGINT NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION task_runs_recovery_capture_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 83 AND EXISTS (
        SELECT 1 FROM task_runs
        WHERE COALESCE(backup_capture_layout, '') <> ''
           OR COALESCE(backup_capture_root, '') <> ''
           OR COALESCE(backup_capture_manifest, '') <> ''
           OR COALESCE(backup_generation_state, '') <> ''
           OR COALESCE(backup_source_run_id, 0) <> 0
    ) THEN
        RAISE EXCEPTION '000083 downgrade blocked: Rsync recovery capture evidence exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_runs_recovery_capture_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_task_runs_recovery_capture_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION task_runs_recovery_capture_downgrade_admission();

COMMIT;
