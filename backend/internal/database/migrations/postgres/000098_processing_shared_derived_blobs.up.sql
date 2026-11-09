BEGIN;
ALTER TABLE backup_asset_processing_uploads DROP CONSTRAINT backup_asset_processing_uploads_staging_id_key;
CREATE INDEX idx_backup_asset_processing_uploads_staging_id ON backup_asset_processing_uploads(staging_id, state);
ALTER TABLE backup_asset_derived_artifact_sets DROP CONSTRAINT backup_asset_derived_artifact_sets_manifest_digest_key;

CREATE FUNCTION processing_shared_derived_blobs_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 98 AND (EXISTS (SELECT 1 FROM backup_asset_processing_uploads GROUP BY staging_id HAVING COUNT(*) > 1)
        OR EXISTS (SELECT 1 FROM backup_asset_derived_artifact_sets GROUP BY manifest_digest HAVING COUNT(*) > 1)) THEN
        RAISE EXCEPTION '000098 downgrade blocked: shared derived staging_id or manifest_digest exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_processing_shared_derived_blobs_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION processing_shared_derived_blobs_downgrade_admission();
COMMIT;
