BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM recovery_point_lifecycle_attempts
        WHERE blocked_reason = 'provider_native_version_referenced'
    ) THEN
        RAISE EXCEPTION '000076 downgrade blocked: provider native version reference reason exists';
    END IF;
END
$$;
DROP TRIGGER IF EXISTS trg_provider_native_version_reference_reason_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS provider_native_version_reference_reason_downgrade_admission();

ALTER TABLE recovery_point_lifecycle_attempts
    DROP CONSTRAINT recovery_point_lifecycle_attempts_blocked_reason_check;
ALTER TABLE recovery_point_lifecycle_attempts
    ADD CONSTRAINT recovery_point_lifecycle_attempts_blocked_reason_check
    CHECK (blocked_reason IN ('', 'active_hold', 'lease_live', 'lease_drain_unproven', 'owner_cleanup_unproven', 'provider_worm', 'provider_unavailable', 'provider_identity_conflict', 'provider_delete_unproven', 'deletion_unavailable', 'fence_lost'));

COMMIT;
