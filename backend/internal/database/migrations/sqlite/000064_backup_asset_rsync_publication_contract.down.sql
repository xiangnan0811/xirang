CREATE TEMP TABLE backup_asset_064_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_064_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM backup_asset_managed_history_latches)
    OR EXISTS (SELECT 1 FROM recovery_points WHERE semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline'))
    OR EXISTS (
        SELECT 1 FROM task_repository_links
        WHERE unlinked_at IS NULL
          AND publication_mode IN ('native_snapshot', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions')
    )
    OR EXISTS (
        SELECT 1 FROM recovery_point_leases
        WHERE holder_type IN ('point_publication', 'rsync_parent')
          AND status = 'active'
    )
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_064_down_guard;

DROP INDEX idx_recovery_points_managed_tree_source_unique;
DROP INDEX idx_backup_asset_managed_history_latches_repository_unique;
DROP INDEX idx_backup_asset_managed_history_latches_installation_unique;
DROP TABLE backup_asset_managed_history_latches;
