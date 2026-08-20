-- Direct execution and nonstandard migration drivers still fail closed here;
-- the metadata admission trigger normally rejects before this body starts.
-- schema_migrations is protected by trg_backup_asset_lifecycle_downgrade_admission.
CREATE TEMP TABLE backup_asset_070_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_070_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM backup_retention_policies)
    OR EXISTS (SELECT 1 FROM recovery_point_holds)
    OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_attempts)
    OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_tombstones)
    OR EXISTS (SELECT 1 FROM backup_repository_import_candidates)
    OR EXISTS (SELECT 1 FROM backup_asset_purge_plans)
    OR EXISTS (SELECT 1 FROM backup_asset_purge_plan_items)
    OR EXISTS (SELECT 1 FROM backup_asset_config_import_refs)
    OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'retention_worker')
    OR EXISTS (SELECT 1 FROM recovery_points WHERE point_revision <> 1)
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_070_down_guard;

DROP TRIGGER trg_backup_asset_lifecycle_downgrade_admission;
DROP TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_delete;
DROP TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_update;
DROP TRIGGER trg_recovery_point_holds_release_one_way;
DROP TRIGGER trg_recovery_points_point_revision_advance;
DROP TRIGGER trg_recovery_points_point_revision_guard;

DROP TABLE backup_asset_config_import_refs;
DROP TABLE backup_repository_import_candidates;
DROP TABLE recovery_point_lifecycle_tombstones;
DROP TABLE recovery_point_lifecycle_attempts;
DROP TABLE backup_asset_purge_plan_items;
DROP TABLE backup_asset_purge_plans;
DROP TABLE recovery_point_holds;
DROP TABLE backup_retention_policies;

ALTER TABLE recovery_points DROP COLUMN point_revision;

-- Restore the exact 000069 closed holder definition without rebuilding the
-- parent table or disturbing 000066-000069 foreign-key references and rows.
PRAGMA writable_schema = ON;
UPDATE sqlite_schema
SET sql = replace(sql, '''search_index'', ''retention_worker''))', '''search_index''))')
WHERE type = 'table'
  AND name = 'recovery_point_leases'
  AND sql LIKE '%''search_index'', ''retention_worker''))%';
PRAGMA writable_schema = RESET;
