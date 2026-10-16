-- Additive only: preserve all existing identity and business state.
ALTER TABLE users ADD COLUMN totp_reenrollment_required BOOLEAN NOT NULL DEFAULT FALSE
    CHECK (totp_reenrollment_required IN (0, 1));

-- No user foreign key: recovery evidence survives account deletion.
CREATE TABLE account_recovery_audits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id VARCHAR(32) NOT NULL CHECK (length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
    target_user_id INTEGER NOT NULL CHECK (target_user_id > 0),
    target_username VARCHAR(64) NOT NULL,
    event VARCHAR(32) NOT NULL CHECK (event IN ('reset', 'reenrollment_completed')),
    reset_password BOOLEAN NOT NULL CHECK (reset_password IN (0, 1)),
    reset_totp BOOLEAN NOT NULL CHECK (reset_totp IN (0, 1)),
    reason TEXT NOT NULL,
    old_token_version INTEGER NOT NULL CHECK (old_token_version >= 0),
    new_token_version INTEGER NOT NULL CHECK (new_token_version = old_token_version + 1),
    revoked_grant_count INTEGER NOT NULL CHECK (revoked_grant_count >= 0),
    created_at DATETIME NOT NULL,
    CHECK ((event = 'reset' AND (reset_password OR reset_totp) AND length(CAST(reason AS BLOB)) BETWEEN 8 AND 512)
        OR (event = 'reenrollment_completed' AND reason = ''))
);
CREATE UNIQUE INDEX idx_account_recovery_audits_operation_id ON account_recovery_audits(operation_id);
CREATE INDEX idx_account_recovery_audits_target_user_id ON account_recovery_audits(target_user_id);
CREATE INDEX idx_account_recovery_audits_created_at ON account_recovery_audits(created_at);

-- Reject before the driver replaces clean metadata with the dirty down target.
CREATE TRIGGER trg_account_recovery_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 94 AND (
    EXISTS (SELECT 1 FROM account_recovery_audits)
    OR EXISTS (SELECT 1 FROM users WHERE totp_reenrollment_required = TRUE)
)
BEGIN
    SELECT RAISE(ABORT, '000094 downgrade blocked: account recovery state exists');
END;
CREATE TRIGGER trg_account_recovery_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 94 AND (
    EXISTS (SELECT 1 FROM account_recovery_audits)
    OR EXISTS (SELECT 1 FROM users WHERE totp_reenrollment_required = TRUE)
)
BEGIN
    SELECT RAISE(ABORT, '000094 downgrade blocked: account recovery state exists');
END;
