BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_managed_history_latches) THEN
        RAISE EXCEPTION '000064 down blocked: managed-history latch exists';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recovery_points
        WHERE semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline')
    ) THEN
        RAISE EXCEPTION '000064 down blocked: managed recovery point exists';
    END IF;
    IF EXISTS (
        SELECT 1 FROM task_repository_links
        WHERE unlinked_at IS NULL
          AND publication_mode IN ('native_snapshot', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions')
    ) THEN
        RAISE EXCEPTION '000064 down blocked: managed repository link exists';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recovery_point_leases
        WHERE holder_type IN ('point_publication', 'rsync_parent')
          AND status = 'active'
    ) THEN
        RAISE EXCEPTION '000064 down blocked: active managed publication lease exists';
    END IF;
END $$;

DROP INDEX idx_recovery_points_managed_tree_source_unique;
DROP INDEX idx_backup_asset_managed_history_latches_repository_unique;
DROP INDEX idx_backup_asset_managed_history_latches_installation_unique;
DROP TABLE backup_asset_managed_history_latches;

COMMIT;
