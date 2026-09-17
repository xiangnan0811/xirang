BEGIN;

-- Direct/manual downgrades must enforce the same safety boundary as the
-- schema_migrations admission trigger. Recovery evidence and live identity
-- state are never silently discarded.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM break_glass_audits)
       OR EXISTS (SELECT 1 FROM auth_pending_tokens)
       OR EXISTS (
           SELECT 1 FROM users
           WHERE COALESCE(totp_enrollment_id, '') <> ''
              OR totp_enrollment_expires_at IS NOT NULL
       ) THEN
        RAISE EXCEPTION '000078 downgrade blocked: identity security state exists';
    END IF;
END;
$$;

DROP TRIGGER IF EXISTS trg_identity_security_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS identity_security_downgrade_admission();
DROP INDEX IF EXISTS idx_break_glass_audits_created_at;
DROP INDEX IF EXISTS idx_break_glass_audits_target_user_id;
DROP TABLE IF EXISTS break_glass_audits;
DROP INDEX IF EXISTS idx_auth_pending_tokens_expires_at;
DROP INDEX IF EXISTS idx_auth_pending_tokens_user_id;
DROP TABLE IF EXISTS auth_pending_tokens;
DROP INDEX IF EXISTS idx_users_totp_enrollment_expires_at;
DROP INDEX IF EXISTS idx_users_totp_enrollment_id;
ALTER TABLE users DROP COLUMN IF EXISTS totp_enrollment_expires_at;
ALTER TABLE users DROP COLUMN IF EXISTS totp_enrollment_id;

COMMIT;
