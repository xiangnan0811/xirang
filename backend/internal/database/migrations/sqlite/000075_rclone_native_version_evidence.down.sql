DROP TABLE IF EXISTS rclone_native_version_evidence_000075_down_guard;
CREATE TEMP TABLE rclone_native_version_evidence_000075_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO rclone_native_version_evidence_000075_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM recovery_point_rclone_native_versions
) THEN 0 ELSE 1 END;
DROP TABLE rclone_native_version_evidence_000075_down_guard;

DROP TRIGGER IF EXISTS trg_rclone_native_version_evidence_downgrade_admission;
DROP INDEX IF EXISTS idx_recovery_point_rclone_native_versions_point_role_ordinal;
DROP INDEX IF EXISTS idx_recovery_point_rclone_native_versions_repository_role_identity_point;
DROP INDEX IF EXISTS idx_recovery_point_rclone_native_versions_point_role_identity;
DROP TABLE IF EXISTS recovery_point_rclone_native_versions;
