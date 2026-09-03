DROP TABLE IF EXISTS recovery_point_lifecycle_attempts_000076_down_guard;
CREATE TEMP TABLE recovery_point_lifecycle_attempts_000076_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO recovery_point_lifecycle_attempts_000076_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM recovery_point_lifecycle_attempts
    WHERE blocked_reason = 'provider_native_version_referenced'
) THEN 0 ELSE 1 END;
DROP TABLE recovery_point_lifecycle_attempts_000076_down_guard;
DROP TRIGGER IF EXISTS trg_provider_native_version_reference_reason_downgrade_admission;
DROP TRIGGER trg_backup_asset_lifecycle_downgrade_admission;

CREATE TABLE recovery_point_lifecycle_attempts_new (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32 AND recovery_point_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    operation TEXT NOT NULL CHECK (operation IN ('retention_expire', 'explicit_purge', 'mutable_retire')),
    phase TEXT NOT NULL CHECK (phase IN ('selected', 'revoking', 'draining', 'cleaning', 'provider_delete', 'tombstoning', 'blocked', 'complete')),
    transition_revision INTEGER NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    policy_id TEXT CHECK (policy_id IS NULL OR (length(policy_id) = 32 AND policy_id NOT GLOB '*[^0-9a-f]*')),
    policy_revision INTEGER CHECK (policy_revision IS NULL OR policy_revision > 0),
    policy_rule_digest TEXT CHECK (policy_rule_digest IS NULL OR (length(policy_rule_digest) = 64 AND policy_rule_digest NOT GLOB '*[^0-9a-f]*')),
    evaluation_time DATETIME,
    purge_plan_id TEXT CHECK (purge_plan_id IS NULL OR (length(purge_plan_id) = 32 AND purge_plan_id NOT GLOB '*[^0-9a-f]*')),
    purge_plan_revision INTEGER CHECK (purge_plan_revision IS NULL OR purge_plan_revision > 0),
    purge_actor_id INTEGER REFERENCES users(id) ON DELETE RESTRICT,
    lease_id TEXT CHECK (lease_id IS NULL OR (length(lease_id) = 32 AND lease_id NOT GLOB '*[^0-9a-f]*')),
    lease_attempt_id TEXT CHECK (lease_attempt_id IS NULL OR (length(lease_attempt_id) = 32 AND lease_attempt_id NOT GLOB '*[^0-9a-f]*')),
    lease_fence_token_hash TEXT CHECK (lease_fence_token_hash IS NULL OR (length(lease_fence_token_hash) = 64 AND lease_fence_token_hash NOT GLOB '*[^0-9a-f]*')),
    blocked_reason TEXT NOT NULL DEFAULT '' CHECK (blocked_reason IN ('', 'active_hold', 'lease_live', 'lease_drain_unproven', 'owner_cleanup_unproven', 'provider_worm', 'provider_unavailable', 'provider_identity_conflict', 'provider_delete_unproven', 'deletion_unavailable', 'fence_lost')),
    claimed_at DATETIME,
    heartbeat_at DATETIME,
    retry_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((policy_id IS NULL AND policy_revision IS NULL AND policy_rule_digest IS NULL AND evaluation_time IS NULL)
        OR (policy_id IS NOT NULL AND policy_revision IS NOT NULL AND policy_rule_digest IS NOT NULL AND evaluation_time IS NOT NULL)),
    CHECK ((purge_plan_id IS NULL AND purge_plan_revision IS NULL AND purge_actor_id IS NULL)
        OR (operation = 'explicit_purge' AND purge_plan_id IS NOT NULL AND purge_plan_revision IS NOT NULL AND purge_actor_id IS NOT NULL)),
    CHECK ((lease_id IS NULL AND lease_attempt_id IS NULL AND lease_fence_token_hash IS NULL)
        OR (lease_id IS NOT NULL AND lease_attempt_id IS NOT NULL AND lease_fence_token_hash IS NOT NULL)),
    CHECK ((phase = 'blocked' AND blocked_reason <> '') OR (phase <> 'blocked' AND blocked_reason = '')),
    CHECK ((phase = 'complete' AND completed_at IS NOT NULL) OR (phase <> 'complete' AND completed_at IS NULL)),
    FOREIGN KEY (policy_id) REFERENCES backup_retention_policies(id) ON DELETE RESTRICT,
    FOREIGN KEY (purge_plan_id, purge_plan_revision) REFERENCES backup_asset_purge_plans(id, revision) ON DELETE RESTRICT,
    FOREIGN KEY (lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT
);

INSERT INTO recovery_point_lifecycle_attempts_new
SELECT * FROM recovery_point_lifecycle_attempts;
DROP TABLE recovery_point_lifecycle_attempts;
ALTER TABLE recovery_point_lifecycle_attempts_new RENAME TO recovery_point_lifecycle_attempts;
CREATE TRIGGER trg_backup_asset_lifecycle_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 70 AND (
    EXISTS (SELECT 1 FROM backup_retention_policies)
    OR EXISTS (SELECT 1 FROM recovery_point_holds)
    OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_attempts)
    OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_tombstones)
    OR EXISTS (SELECT 1 FROM backup_repository_import_candidates)
    OR EXISTS (SELECT 1 FROM backup_asset_purge_plans)
    OR EXISTS (SELECT 1 FROM backup_asset_purge_plan_items)
    OR EXISTS (SELECT 1 FROM backup_asset_config_import_refs)
    OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'retention_worker')
    OR EXISTS (SELECT 1 FROM recovery_points WHERE point_revision <> 1)
)
BEGIN
    SELECT RAISE(ABORT, '000070 downgrade blocked: backup asset lifecycle state exists');
END;

CREATE UNIQUE INDEX idx_recovery_point_lifecycle_attempts_active
    ON recovery_point_lifecycle_attempts(recovery_point_id)
    WHERE phase <> 'complete';
CREATE INDEX idx_recovery_point_lifecycle_attempts_retry
    ON recovery_point_lifecycle_attempts(phase, retry_at, updated_at);
