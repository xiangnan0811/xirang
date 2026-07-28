CREATE TEMP TABLE backup_asset_068_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_068_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM backup_asset_export_jobs)
    OR EXISTS (SELECT 1 FROM backup_asset_export_keys)
    OR EXISTS (SELECT 1 FROM backup_asset_export_items)
    OR EXISTS (SELECT 1 FROM backup_asset_export_attempts)
    OR EXISTS (SELECT 1 FROM backup_asset_export_item_attempts)
    OR EXISTS (SELECT 1 FROM backup_asset_export_source_leases)
    OR EXISTS (SELECT 1 FROM backup_asset_export_artifacts)
    OR EXISTS (SELECT 1 FROM backup_asset_export_idempotency)
    OR EXISTS (SELECT 1 FROM backup_asset_export_quota_buckets)
    OR EXISTS (SELECT 1 FROM backup_asset_export_reservations)
    OR EXISTS (SELECT 1 FROM backup_asset_export_delivery_grants)
    OR EXISTS (SELECT 1 FROM backup_asset_export_delivery_requests)
    OR EXISTS (SELECT 1 FROM backup_asset_archive_member_requests)
    OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'export_job')
    OR EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'export_store')
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_068_down_guard;

DROP TABLE backup_asset_export_delivery_requests;
DROP TABLE backup_asset_export_delivery_grants;
DROP TABLE backup_asset_archive_member_requests;
DROP TABLE backup_asset_export_reservations;
DROP TABLE backup_asset_export_quota_buckets;
DROP TABLE backup_asset_export_idempotency;
DROP TABLE backup_asset_export_artifacts;
DROP TABLE backup_asset_export_source_leases;
DROP TABLE backup_asset_export_item_attempts;
DROP TABLE backup_asset_export_attempts;
DROP TABLE backup_asset_export_items;
DROP TABLE backup_asset_export_keys;
DROP TABLE backup_asset_export_jobs;

CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store')),
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
