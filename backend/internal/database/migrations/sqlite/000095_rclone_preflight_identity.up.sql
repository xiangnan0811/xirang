CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store', 'export_store', 'rclone_preflight_identity')),
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
     wrapping_key_fingerprint, activated_at, verify_until, lost_at, created_at, updated_at)
SELECT id, domain, version, state, wrapped_key, wrap_algorithm,
       wrapping_key_fingerprint, activated_at, verify_until, lost_at, created_at, updated_at
FROM wrapped_domain_keys;
DROP TABLE wrapped_domain_keys;
ALTER TABLE wrapped_domain_keys_new RENAME TO wrapped_domain_keys;
CREATE UNIQUE INDEX idx_wrapped_domain_keys_active ON wrapped_domain_keys(domain) WHERE state = 'active';
CREATE INDEX idx_wrapped_domain_keys_domain_state ON wrapped_domain_keys(domain, state);

-- Migrate replaces metadata before running down SQL; reject the dirty target first.
CREATE TRIGGER trg_rclone_preflight_identity_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 95 AND EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'rclone_preflight_identity')
BEGIN
    SELECT RAISE(ABORT, '000095 downgrade blocked: Rclone preflight identity key exists');
END;
CREATE TRIGGER trg_rclone_preflight_identity_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 95 AND EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'rclone_preflight_identity')
BEGIN
    SELECT RAISE(ABORT, '000095 downgrade blocked: Rclone preflight identity key exists');
END;
