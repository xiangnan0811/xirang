-- Normalize the published 000069 TaskRun snapshot contract. Positive snapshots
-- belong to ordinary runs; node_id_snapshot=0 is migration-owned legacy_unknown
-- and is closed to terminal states: success, failed, canceled, warning, skipped.
CREATE TEMP TABLE backup_asset_072_task_run_snapshot_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO backup_asset_072_task_run_snapshot_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM task_runs
    WHERE node_id_snapshot IS NULL
       OR node_id_snapshot < 0
       OR (
            node_id_snapshot = 0
            AND status NOT IN ('success', 'failed', 'canceled', 'warning', 'skipped')
       )
) THEN 0 ELSE 1 END;
DROP TABLE backup_asset_072_task_run_snapshot_guard;

DROP TRIGGER IF EXISTS trg_backup_asset_recovery_task_runs_node_snapshot_insert;
CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_insert
BEFORE INSERT ON task_runs
WHEN NEW.node_id_snapshot IS NULL
    OR NEW.node_id_snapshot <= 0
    OR NOT EXISTS (
        SELECT 1 FROM tasks
        WHERE tasks.id = NEW.task_id
          AND tasks.node_id = NEW.node_id_snapshot
          AND tasks.node_id > 0
    )
BEGIN
    SELECT RAISE(ABORT, 'TaskRun node snapshot must match the Task node at creation');
END;

DROP TRIGGER IF EXISTS trg_backup_asset_recovery_task_runs_node_snapshot_immutable;
CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_immutable
BEFORE UPDATE OF task_id, node_id_snapshot ON task_runs
WHEN NEW.task_id IS NOT OLD.task_id
    OR NEW.node_id_snapshot IS NOT OLD.node_id_snapshot
BEGIN
    SELECT RAISE(ABORT, 'TaskRun task and node snapshot are immutable');
END;

DROP TRIGGER IF EXISTS trg_backup_asset_task_runs_legacy_unknown_status_immutable;
CREATE TRIGGER trg_backup_asset_task_runs_legacy_unknown_status_immutable
BEFORE UPDATE OF status ON task_runs
WHEN OLD.node_id_snapshot = 0 AND NEW.status IS NOT OLD.status
BEGIN
    SELECT RAISE(ABORT, 'legacy_unknown TaskRun status is immutable');
END;

-- golang-migrate writes target=71, dirty=true before executing the down body.
-- Reject that metadata write while a legacy_unknown row exists so version 72
-- remains clean and the schema is unchanged.
DROP TRIGGER IF EXISTS trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission;
CREATE TRIGGER trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 72
    AND EXISTS (SELECT 1 FROM task_runs WHERE node_id_snapshot = 0)
BEGIN
    SELECT RAISE(ABORT, '000072 downgrade blocked: legacy_unknown TaskRun history exists');
END;
