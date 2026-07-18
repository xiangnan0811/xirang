CREATE TEMP TABLE backup_asset_065_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_065_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'search_token')
    OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'search_index')
    OR EXISTS (SELECT 1 FROM backup_asset_search_generations)
    OR EXISTS (SELECT 1 FROM backup_asset_search_documents)
    OR EXISTS (SELECT 1 FROM backup_asset_search_postings)
    OR EXISTS (SELECT 1 FROM backup_asset_search_document_fields)
    OR EXISTS (SELECT 1 FROM backup_asset_saved_searches)
    OR EXISTS (SELECT 1 FROM backup_asset_saved_search_scope_points)
    OR EXISTS (SELECT 1 FROM backup_asset_favorites)
    OR EXISTS (SELECT 1 FROM backup_asset_tag_definitions)
    OR EXISTS (SELECT 1 FROM backup_asset_tag_assignments)
    OR EXISTS (SELECT 1 FROM backup_asset_recent_access)
    OR EXISTS (SELECT 1 FROM backup_asset_overlay_usage)
    OR EXISTS (SELECT 1 FROM backup_asset_overlay_idempotency)
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_065_down_guard;

DROP TABLE backup_asset_overlay_idempotency;
DROP TABLE backup_asset_overlay_usage;
DROP TABLE backup_asset_recent_access;
DROP TABLE backup_asset_tag_assignments;
DROP TABLE backup_asset_tag_definitions;
DROP TABLE backup_asset_favorites;
DROP TABLE backup_asset_saved_search_scope_points;
DROP TABLE backup_asset_saved_searches;
DROP TABLE backup_asset_search_document_fields;
DROP TABLE backup_asset_search_postings;
DROP TABLE backup_asset_search_documents;
DROP TABLE backup_asset_search_generations;

DROP INDEX idx_catalog_entries_generation_entry_recovery_point;
DROP INDEX idx_catalog_generations_id_recovery_point;

CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership')),
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
    ON wrapped_domain_keys(domain)
    WHERE state = 'active';
CREATE INDEX idx_wrapped_domain_keys_domain_state
    ON wrapped_domain_keys(domain, state);

CREATE TABLE recovery_point_leases_new (
    id TEXT PRIMARY KEY,
    recovery_point_id TEXT NOT NULL,
    holder_type TEXT NOT NULL CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication')),
    owner_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    fence_token TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'released', 'expired')),
    lease_expires_at DATETIME NOT NULL,
    absolute_deadline DATETIME NOT NULL,
    last_heartbeat_at DATETIME NOT NULL,
    released_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE
);

INSERT INTO recovery_point_leases_new
    (id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token,
     status, lease_expires_at, absolute_deadline, last_heartbeat_at,
     released_at, created_at, updated_at)
SELECT id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token,
       status, lease_expires_at, absolute_deadline, last_heartbeat_at,
       released_at, created_at, updated_at
FROM recovery_point_leases;

DROP TABLE recovery_point_leases;
ALTER TABLE recovery_point_leases_new RENAME TO recovery_point_leases;
CREATE UNIQUE INDEX idx_recovery_point_leases_active_owner_slot
    ON recovery_point_leases(recovery_point_id, holder_type, owner_id)
    WHERE status = 'active';
CREATE INDEX idx_recovery_point_leases_recovery_status_expiry
    ON recovery_point_leases(recovery_point_id, status, lease_expires_at);
CREATE INDEX idx_recovery_point_leases_absolute_deadline
    ON recovery_point_leases(absolute_deadline);
