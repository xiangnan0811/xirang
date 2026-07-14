BEGIN;

ALTER TABLE task_repository_links
    DROP CONSTRAINT task_repository_links_publication_mode_check;
ALTER TABLE task_repository_links
    ADD CONSTRAINT task_repository_links_publication_mode_check
    CHECK (publication_mode IN ('legacy_mutable', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions', 'native_snapshot'));

UPDATE task_repository_links AS link
SET publication_mode = 'native_snapshot'
FROM backup_repositories AS repository
WHERE repository.id = link.repository_id
  AND repository.provider_kind = 'restic'
  AND link.publication_mode = 'native_object_versions';

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication'));

CREATE UNIQUE INDEX idx_recovery_points_producing_task_run_unique
    ON recovery_points(producing_task_run_id)
    WHERE producing_task_run_id IS NOT NULL;
CREATE UNIQUE INDEX idx_recovery_points_native_source_unique
    ON recovery_points(repository_id, source_fingerprint)
    WHERE semantics = 'native_snapshot' AND source_fingerprint <> '';

COMMIT;
