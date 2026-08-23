-- Both supported upgrade paths must already contain only positive snapshots or
-- migration-owned legacy_unknown=0 terminal history.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM task_runs
        WHERE node_id_snapshot IS NULL
           OR node_id_snapshot < 0
           OR (
                node_id_snapshot = 0
                AND status NOT IN ('success', 'failed', 'canceled', 'warning', 'skipped')
           )
    ) THEN
        RAISE EXCEPTION '000072 TaskRun snapshot compatibility rejected invalid rows';
    END IF;
END
$$;

ALTER TABLE task_runs
    DROP CONSTRAINT IF EXISTS task_runs_node_id_snapshot_positive,
    DROP CONSTRAINT IF EXISTS task_runs_node_id_snapshot_compatibility;
ALTER TABLE task_runs
    ADD CONSTRAINT task_runs_node_id_snapshot_compatibility CHECK (
        node_id_snapshot > 0
        OR (
            node_id_snapshot = 0
            AND status IN ('success', 'failed', 'canceled', 'warning', 'skipped')
        )
    );

CREATE OR REPLACE FUNCTION backup_asset_recovery_task_run_node_snapshot_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    task_node_id BIGINT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.task_id IS DISTINCT FROM OLD.task_id
           OR NEW.node_id_snapshot IS DISTINCT FROM OLD.node_id_snapshot THEN
            RAISE EXCEPTION 'TaskRun task and node snapshot are immutable';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.node_id_snapshot IS NULL OR NEW.node_id_snapshot <= 0 THEN
        RAISE EXCEPTION 'TaskRun node snapshot must be positive';
    END IF;
    SELECT node_id INTO task_node_id
    FROM tasks
    WHERE id = NEW.task_id
    FOR SHARE;
    IF NOT FOUND OR task_node_id <= 0 OR task_node_id IS DISTINCT FROM NEW.node_id_snapshot THEN
        RAISE EXCEPTION 'TaskRun node snapshot must match the Task node at creation';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_asset_recovery_task_runs_node_snapshot_insert ON task_runs;
CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_insert
BEFORE INSERT ON task_runs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_task_run_node_snapshot_guard();
DROP TRIGGER IF EXISTS trg_backup_asset_recovery_task_runs_node_snapshot_immutable ON task_runs;
CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_immutable
BEFORE UPDATE OF task_id, node_id_snapshot ON task_runs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_task_run_node_snapshot_guard();

CREATE OR REPLACE FUNCTION backup_asset_task_run_legacy_unknown_status_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.node_id_snapshot = 0 AND NEW.status IS DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION 'legacy_unknown TaskRun status is immutable';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_asset_task_runs_legacy_unknown_status_immutable ON task_runs;
CREATE TRIGGER trg_backup_asset_task_runs_legacy_unknown_status_immutable
BEFORE UPDATE OF status ON task_runs
FOR EACH ROW EXECUTE FUNCTION backup_asset_task_run_legacy_unknown_status_guard();

CREATE OR REPLACE FUNCTION backup_asset_task_run_snapshot_compatibility_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 72
       AND EXISTS (SELECT 1 FROM task_runs WHERE node_id_snapshot = 0) THEN
        RAISE EXCEPTION '000072 downgrade blocked: legacy_unknown TaskRun history exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION backup_asset_task_run_snapshot_compatibility_downgrade_admission();
