BEGIN;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'rclone_preflight_identity') THEN
        RAISE EXCEPTION '000095 downgrade blocked: Rclone preflight identity key exists';
    END IF;
END;
$$;
DROP TRIGGER IF EXISTS trg_rclone_preflight_identity_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS rclone_preflight_identity_downgrade_admission();
ALTER TABLE wrapped_domain_keys DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store', 'export_store'));
COMMIT;
