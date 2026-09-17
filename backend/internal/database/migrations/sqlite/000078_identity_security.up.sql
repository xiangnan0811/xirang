
-- Historical setup wrote an unverified secret directly to users.totp_secret.
-- Active TOTP secrets are retained; all unverified legacy enrollments must
-- restart through the encrypted enrollment flow below.
UPDATE users
SET totp_secret = ''
WHERE COALESCE(totp_enabled, 0) = 0
  AND COALESCE(totp_secret, '') <> '';

ALTER TABLE users ADD COLUMN totp_enrollment_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_enrollment_expires_at DATETIME;

CREATE INDEX IF NOT EXISTS idx_users_totp_enrollment_id ON users(totp_enrollment_id);
CREATE INDEX IF NOT EXISTS idx_users_totp_enrollment_expires_at ON users(totp_enrollment_expires_at);

CREATE TABLE IF NOT EXISTS auth_pending_tokens (
    jti VARCHAR(64) PRIMARY KEY NOT NULL,
    user_id INTEGER NOT NULL,
    token_version INTEGER NOT NULL DEFAULT 0,
    totp_binding VARCHAR(64) NOT NULL,
    expires_at DATETIME NOT NULL,
    consumed_at DATETIME,
    created_at DATETIME NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_auth_pending_tokens_user_id ON auth_pending_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_auth_pending_tokens_expires_at ON auth_pending_tokens(expires_at);

-- Keep recovery evidence after the target account is removed. Audit history
-- must not become an accidental user-deletion blocker.
CREATE TABLE IF NOT EXISTS break_glass_audits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    target_user_id INTEGER NOT NULL,
    target_username VARCHAR(64) NOT NULL,
    reason TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_break_glass_audits_target_user_id ON break_glass_audits(target_user_id);
CREATE INDEX IF NOT EXISTS idx_break_glass_audits_created_at ON break_glass_audits(created_at);

-- Metadata admission runs before the migration driver records a downgrade.
-- Durable recovery evidence, pending challenges and active enrollments must
-- never be erased by rolling the schema below version 78.
DROP TRIGGER IF EXISTS trg_identity_security_downgrade_admission;
CREATE TRIGGER trg_identity_security_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 78
 AND (
    EXISTS (SELECT 1 FROM break_glass_audits)
    OR EXISTS (SELECT 1 FROM auth_pending_tokens)
    OR EXISTS (
        SELECT 1 FROM users
        WHERE COALESCE(totp_enrollment_id, '') <> ''
           OR totp_enrollment_expires_at IS NOT NULL
    )
 )
BEGIN
    SELECT RAISE(ABORT, '000078 downgrade blocked: identity security state exists');
END;
