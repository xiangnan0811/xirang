CREATE TEMP TABLE backup_asset_067_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_067_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM backup_asset_processing_jobs)
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
    )
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_067_down_guard;

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

CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token')),
    version INTEGER NOT NULL CHECK (version > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'verify_only', 'retired', 'lost')),
    wrapped_key TEXT NOT NULL,
    wrap_algorithm TEXT NOT NULL,
    wrapping_key_fingerprint TEXT NOT NULL,
    activated_at DATETIME NOT NULL,
    verify_until DATETIME,
    lost_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (domain, version)
);

INSERT INTO wrapped_domain_keys_new
    (id, domain, version, state, wrapped_key, wrap_algorithm,
     wrapping_key_fingerprint, activated_at, verify_until, lost_at,
     created_at, updated_at)
SELECT id, domain, version, state, wrapped_key, wrap_algorithm,
       wrapping_key_fingerprint, activated_at, verify_until, lost_at,
       created_at, updated_at
FROM wrapped_domain_keys;

DROP TABLE wrapped_domain_keys;
ALTER TABLE wrapped_domain_keys_new RENAME TO wrapped_domain_keys;
CREATE UNIQUE INDEX idx_wrapped_domain_keys_active
    ON wrapped_domain_keys(domain) WHERE state = 'active';
CREATE INDEX idx_wrapped_domain_keys_domain_state
    ON wrapped_domain_keys(domain, state);
