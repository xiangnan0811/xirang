-- Keep the SQL independently guarded even when metadata guards are absent.
CREATE TEMP TABLE rclone_preflight_identity_000095_down_guard (valid INTEGER NOT NULL CHECK (valid = 1));
INSERT INTO rclone_preflight_identity_000095_down_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'rclone_preflight_identity') THEN 0 ELSE 1 END;
DROP TABLE rclone_preflight_identity_000095_down_guard;
DROP TRIGGER IF EXISTS trg_rclone_preflight_identity_downgrade_update_admission;
DROP TRIGGER IF EXISTS trg_rclone_preflight_identity_downgrade_admission;

CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store', 'export_store')),
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
