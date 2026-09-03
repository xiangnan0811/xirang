BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM recovery_point_rclone_native_versions
    ) THEN
        RAISE EXCEPTION '000075 downgrade blocked: native Rclone version evidence exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_rclone_native_version_evidence_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS rclone_native_version_evidence_downgrade_admission();
DROP INDEX IF EXISTS idx_recovery_point_rclone_native_versions_point_role_ordinal;
DROP INDEX IF EXISTS idx_recovery_point_rclone_native_versions_repository_role_identity_point;
DROP INDEX IF EXISTS idx_recovery_point_rclone_native_versions_point_role_identity;
DROP TABLE IF EXISTS recovery_point_rclone_native_versions;

COMMIT;
