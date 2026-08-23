-- The 000069 insert/identity defenses remain owned by
-- trg_backup_asset_recovery_task_runs_node_snapshot_insert and
-- trg_backup_asset_recovery_task_runs_node_snapshot_immutable.
-- Only a pristine schema with no node_id_snapshot=0 row may return to the old
-- positive-only contract. Closed terminal values are success, failed,
-- canceled, warning, skipped.
CREATE TEMP TABLE backup_asset_072_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_072_down_guard(allowed)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM task_runs WHERE node_id_snapshot = 0
) THEN 0 ELSE 1 END;
DROP TABLE backup_asset_072_down_guard;

DROP TRIGGER trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission;
DROP TRIGGER trg_backup_asset_task_runs_legacy_unknown_status_immutable;
