BEGIN;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_processing_uploads GROUP BY staging_id HAVING COUNT(*) > 1)
        OR EXISTS (SELECT 1 FROM backup_asset_derived_artifact_sets GROUP BY manifest_digest HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION '000098 downgrade blocked: shared derived staging_id or manifest_digest exists';
    END IF;
END;
$$;
DROP TRIGGER trg_processing_shared_derived_blobs_downgrade_admission ON schema_migrations;
DROP FUNCTION processing_shared_derived_blobs_downgrade_admission();
DROP INDEX idx_backup_asset_processing_uploads_staging_id;
ALTER TABLE backup_asset_processing_uploads ADD CONSTRAINT backup_asset_processing_uploads_staging_id_key UNIQUE (staging_id);
ALTER TABLE backup_asset_derived_artifact_sets ADD CONSTRAINT backup_asset_derived_artifact_sets_manifest_digest_key UNIQUE (manifest_digest);
COMMIT;
