-- 000083 records the logical Rsync capture root, source-side manifest, and
-- mutable-generation state. Historical rows remain unknown and are never
-- guessed into a restore authority.
ALTER TABLE task_runs ADD COLUMN backup_capture_layout VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN backup_capture_root VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN backup_capture_manifest TEXT NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN backup_generation_state VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN backup_source_run_id INTEGER NOT NULL DEFAULT 0;

DROP TRIGGER IF EXISTS trg_task_runs_recovery_capture_downgrade_admission;
CREATE TRIGGER trg_task_runs_recovery_capture_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 83
 AND (
    EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_capture_layout, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_capture_root, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_capture_manifest, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_generation_state, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_source_run_id, 0) <> 0)
 )
BEGIN
    SELECT RAISE(ABORT, '000083 downgrade blocked: Rsync recovery capture evidence exists');
END;
