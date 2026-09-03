CREATE TABLE recovery_point_rclone_native_versions (
    recovery_point_id TEXT NOT NULL,
    evidence_role TEXT NOT NULL CHECK (evidence_role IN ('owned', 'reference')),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    repository_id TEXT NOT NULL,
    identity_digest TEXT NOT NULL CHECK (length(identity_digest) = 64),
    encrypted_physical_key TEXT NOT NULL,
    encrypted_version_id TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (recovery_point_id, evidence_role, ordinal),
    FOREIGN KEY (repository_id) REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_recovery_point_rclone_native_versions_point_role_identity
    ON recovery_point_rclone_native_versions(recovery_point_id, evidence_role, identity_digest);
CREATE INDEX idx_recovery_point_rclone_native_versions_repository_role_identity_point
    ON recovery_point_rclone_native_versions(repository_id, evidence_role, identity_digest, recovery_point_id);
CREATE INDEX idx_recovery_point_rclone_native_versions_point_role_ordinal
    ON recovery_point_rclone_native_versions(recovery_point_id, evidence_role, ordinal);

DROP TRIGGER IF EXISTS trg_rclone_native_version_evidence_downgrade_admission;
CREATE TRIGGER trg_rclone_native_version_evidence_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 75
 AND EXISTS (
    SELECT 1 FROM recovery_point_rclone_native_versions
 )
BEGIN
    SELECT RAISE(ABORT, '000075 downgrade blocked: native Rclone version evidence exists');
END;
