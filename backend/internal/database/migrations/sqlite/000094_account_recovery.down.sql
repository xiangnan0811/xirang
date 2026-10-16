-- Independent guard for direct/manual execution, even without metadata guards.
CREATE TEMP TABLE account_recovery_000094_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO account_recovery_000094_down_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM account_recovery_audits)
    OR EXISTS (SELECT 1 FROM users WHERE totp_reenrollment_required = TRUE)
    THEN 0 ELSE 1 END;
DROP TABLE account_recovery_000094_down_guard;

DROP TRIGGER IF EXISTS trg_account_recovery_downgrade_update_admission;
DROP TRIGGER IF EXISTS trg_account_recovery_downgrade_admission;
DROP INDEX idx_account_recovery_audits_created_at;
DROP INDEX idx_account_recovery_audits_target_user_id;
DROP INDEX idx_account_recovery_audits_operation_id;
DROP TABLE account_recovery_audits;
ALTER TABLE users DROP COLUMN totp_reenrollment_required;
