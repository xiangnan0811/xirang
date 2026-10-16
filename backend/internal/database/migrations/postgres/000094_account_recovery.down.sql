BEGIN;

-- Independent guard for direct/manual execution, even without metadata guards.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM account_recovery_audits)
       OR EXISTS (SELECT 1 FROM users WHERE totp_reenrollment_required = TRUE) THEN
        RAISE EXCEPTION '000094 downgrade blocked: account recovery state exists';
    END IF;
END;
$$;

DROP TRIGGER IF EXISTS trg_account_recovery_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS account_recovery_downgrade_admission();
DROP INDEX idx_account_recovery_audits_created_at;
DROP INDEX idx_account_recovery_audits_target_user_id;
DROP INDEX idx_account_recovery_audits_operation_id;
DROP TABLE account_recovery_audits;
ALTER TABLE users DROP COLUMN totp_reenrollment_required;

COMMIT;
