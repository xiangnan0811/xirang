-- Direct execution and nonstandard migration drivers still fail closed here;
-- the metadata admission trigger normally rejects before this body starts.
-- schema_migrations is protected by trg_backup_asset_ga_downgrade_admission.
-- Closed 000071 values: class fresh|existing; readiness unknown|blocked|ready|acknowledged;
-- conflict kinds shared_restic_identity, task_repository_mismatch, capability_gap, command_unsupported.
CREATE TEMP TABLE backup_asset_071_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_071_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (
        SELECT 1 FROM backup_asset_installations
        WHERE readiness IN ('ready', 'acknowledged')
           OR enablement_succeeded_at IS NOT NULL
    )
    OR EXISTS (SELECT 1 FROM backup_asset_repository_conflicts)
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_071_down_guard;

DROP TRIGGER trg_backup_asset_ga_downgrade_admission;
DROP TABLE backup_asset_repository_conflicts;
DROP TABLE backup_asset_inventory_runs;
DROP TABLE backup_asset_installations;
