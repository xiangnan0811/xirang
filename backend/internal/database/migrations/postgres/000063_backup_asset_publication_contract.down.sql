BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM recovery_point_leases
        WHERE holder_type = 'point_publication' AND status = 'active'
    ) THEN
        RAISE EXCEPTION '000063 down blocked: active point publication lease';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recovery_points WHERE semantics = 'native_snapshot'
    ) THEN
        RAISE EXCEPTION '000063 down blocked: managed Restic history exists';
    END IF;
END $$;

DROP INDEX idx_recovery_points_producing_task_run_unique;
DROP INDEX idx_recovery_points_native_source_unique;

UPDATE task_repository_links AS link
SET publication_mode = 'native_object_versions'
FROM backup_repositories AS repository
WHERE repository.id = link.repository_id
  AND repository.provider_kind = 'restic'
  AND link.publication_mode = 'native_snapshot';

ALTER TABLE task_repository_links
    DROP CONSTRAINT task_repository_links_publication_mode_check;
ALTER TABLE task_repository_links
    ADD CONSTRAINT task_repository_links_publication_mode_check
    CHECK (publication_mode IN ('legacy_mutable', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions'));

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job'));

COMMIT;
