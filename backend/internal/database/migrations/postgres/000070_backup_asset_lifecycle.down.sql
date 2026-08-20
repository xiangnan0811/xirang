BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_retention_policies)
       OR EXISTS (SELECT 1 FROM recovery_point_holds)
       OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_attempts)
       OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_tombstones)
       OR EXISTS (SELECT 1 FROM backup_repository_import_candidates)
       OR EXISTS (SELECT 1 FROM backup_asset_purge_plans)
       OR EXISTS (SELECT 1 FROM backup_asset_purge_plan_items)
       OR EXISTS (SELECT 1 FROM backup_asset_config_import_refs)
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'retention_worker')
       OR EXISTS (SELECT 1 FROM recovery_points WHERE point_revision <> 1) THEN
        RAISE EXCEPTION '000070 down blocked: backup asset lifecycle state exists';
    END IF;
END
$$;

DROP TRIGGER trg_backup_asset_lifecycle_downgrade_admission ON schema_migrations;
DROP FUNCTION backup_asset_lifecycle_downgrade_admission();
DROP TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_delete ON recovery_point_lifecycle_tombstones;
DROP TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_update ON recovery_point_lifecycle_tombstones;
DROP FUNCTION recovery_point_lifecycle_tombstone_immutable();
DROP TRIGGER trg_recovery_point_holds_release_one_way ON recovery_point_holds;
DROP FUNCTION recovery_point_hold_release_one_way();
DROP TRIGGER trg_recovery_points_point_revision ON recovery_points;
DROP FUNCTION recovery_point_revision_advance();

DROP TABLE backup_asset_config_import_refs;
DROP TABLE backup_repository_import_candidates;
DROP TABLE recovery_point_lifecycle_tombstones;
DROP TABLE recovery_point_lifecycle_attempts;
DROP TABLE backup_asset_purge_plan_items;
DROP TABLE backup_asset_purge_plans;
DROP TABLE recovery_point_holds;
DROP TABLE backup_retention_policies;

ALTER TABLE recovery_points DROP COLUMN point_revision;

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication', 'search_index'));

COMMIT;
