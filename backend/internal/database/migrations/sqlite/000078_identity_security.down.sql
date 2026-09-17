
-- Direct/manual downgrades must enforce the same safety boundary as the
-- schema_migrations admission trigger. Recovery evidence and live identity
-- state are never silently discarded.
CREATE TEMP TABLE identity_security_000078_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO identity_security_000078_down_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM break_glass_audits)
    OR EXISTS (SELECT 1 FROM auth_pending_tokens)
    OR EXISTS (
        SELECT 1 FROM users
        WHERE COALESCE(totp_enrollment_id, '') <> ''
           OR totp_enrollment_expires_at IS NOT NULL
    )
    THEN 0 ELSE 1 END;
DROP TABLE identity_security_000078_down_guard;

DROP TRIGGER IF EXISTS trg_identity_security_downgrade_admission;
DROP INDEX IF EXISTS idx_break_glass_audits_created_at;
DROP INDEX IF EXISTS idx_break_glass_audits_target_user_id;
DROP TABLE IF EXISTS break_glass_audits;
DROP INDEX IF EXISTS idx_auth_pending_tokens_expires_at;
DROP INDEX IF EXISTS idx_auth_pending_tokens_user_id;
DROP TABLE IF EXISTS auth_pending_tokens;
DROP INDEX IF EXISTS idx_users_totp_enrollment_expires_at;
DROP INDEX IF EXISTS idx_users_totp_enrollment_id;
ALTER TABLE users DROP COLUMN totp_enrollment_expires_at;
ALTER TABLE users DROP COLUMN totp_enrollment_id;
