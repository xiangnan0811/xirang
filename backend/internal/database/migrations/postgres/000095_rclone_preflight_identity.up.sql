BEGIN;
ALTER TABLE wrapped_domain_keys DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store', 'export_store', 'rclone_preflight_identity'));

CREATE FUNCTION rclone_preflight_identity_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 95 AND EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'rclone_preflight_identity') THEN
        RAISE EXCEPTION '000095 downgrade blocked: Rclone preflight identity key exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_rclone_preflight_identity_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION rclone_preflight_identity_downgrade_admission();
COMMIT;
