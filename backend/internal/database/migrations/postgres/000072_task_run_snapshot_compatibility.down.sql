-- The 000069 insert/identity defenses remain owned by
-- trg_backup_asset_recovery_task_runs_node_snapshot_insert and
-- trg_backup_asset_recovery_task_runs_node_snapshot_immutable.
-- Closed terminal legacy_unknown states are success, failed, canceled, warning,
-- skipped. The metadata admission trigger normally rejects used down before
-- this body starts.
BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM task_runs WHERE node_id_snapshot = 0) THEN
        RAISE EXCEPTION '000072 down blocked: legacy_unknown TaskRun history exists';
    END IF;
END
$$;

DROP TRIGGER trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission ON schema_migrations;
DROP FUNCTION backup_asset_task_run_snapshot_compatibility_downgrade_admission();
DROP TRIGGER trg_backup_asset_task_runs_legacy_unknown_status_immutable ON task_runs;
DROP FUNCTION backup_asset_task_run_legacy_unknown_status_guard();

ALTER TABLE task_runs
    DROP CONSTRAINT task_runs_node_id_snapshot_compatibility,
    ADD CONSTRAINT task_runs_node_id_snapshot_positive CHECK (node_id_snapshot > 0);

COMMIT;
