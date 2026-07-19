BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_processing_jobs)
       OR EXISTS (SELECT 1 FROM backup_asset_processing_interests)
       OR EXISTS (SELECT 1 FROM backup_asset_processing_attempts)
       OR EXISTS (SELECT 1 FROM backup_asset_processing_grants)
       OR EXISTS (SELECT 1 FROM backup_asset_processing_grant_requests)
       OR EXISTS (SELECT 1 FROM backup_asset_processing_uploads)
       OR EXISTS (SELECT 1 FROM backup_asset_worker_identities)
       OR EXISTS (SELECT 1 FROM backup_asset_worker_capabilities)
       OR EXISTS (SELECT 1 FROM backup_asset_derived_artifact_sets)
       OR EXISTS (SELECT 1 FROM backup_asset_derived_artifacts)
       OR EXISTS (SELECT 1 FROM backup_asset_derived_blobs)
       OR EXISTS (SELECT 1 FROM backup_asset_derived_blob_references)
       OR EXISTS (SELECT 1 FROM backup_asset_updater_metadata)
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'processing_job')
       OR EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'derived_store')
       OR EXISTS (
           SELECT 1 FROM backup_asset_search_postings
           WHERE field IN ('content', 'ocr')
       )
       OR EXISTS (
           SELECT 1 FROM backup_asset_search_document_fields
           WHERE field IN ('content', 'ocr')
             AND (excerpt_ref IS NOT NULL OR state <> 'unavailable')
       ) THEN
        RAISE EXCEPTION '000067 down blocked: processing, derived, lease, key, or projection state exists';
    END IF;
END $$;

DROP TABLE backup_asset_derived_blob_references;
DROP TABLE backup_asset_derived_artifacts;
DROP TABLE backup_asset_derived_artifact_sets;
DROP TABLE backup_asset_derived_blobs;
DROP TABLE backup_asset_processing_uploads;
DROP TABLE backup_asset_processing_grant_requests;
DROP TABLE backup_asset_processing_grants;
DROP TABLE backup_asset_processing_attempts;
DROP TABLE backup_asset_processing_interests;
DROP TABLE backup_asset_processing_jobs;
DROP TABLE backup_asset_worker_capabilities;
DROP TABLE backup_asset_worker_identities;
DROP TABLE backup_asset_updater_metadata;

ALTER TABLE wrapped_domain_keys DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token'));

COMMIT;
