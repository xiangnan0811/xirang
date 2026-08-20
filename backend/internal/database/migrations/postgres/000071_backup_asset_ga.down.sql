-- Closed 000071 values: class fresh|existing; readiness unknown|blocked|ready|acknowledged;
-- conflict kinds shared_restic_identity, task_repository_mismatch, capability_gap, command_unsupported.
BEGIN;

DO $$
BEGIN
    IF EXISTS (
            SELECT 1 FROM backup_asset_installations
            WHERE readiness IN ('ready', 'acknowledged')
               OR enablement_succeeded_at IS NOT NULL
        )
        OR EXISTS (SELECT 1 FROM backup_asset_repository_conflicts) THEN
        RAISE EXCEPTION '000071 down blocked: backup asset GA readiness or conflict state exists';
    END IF;
END
$$;

DROP TRIGGER trg_backup_asset_ga_downgrade_admission ON schema_migrations;
DROP FUNCTION backup_asset_ga_downgrade_admission();
DROP TABLE backup_asset_repository_conflicts;
DROP TABLE backup_asset_inventory_runs;
DROP TABLE backup_asset_installations;

COMMIT;
