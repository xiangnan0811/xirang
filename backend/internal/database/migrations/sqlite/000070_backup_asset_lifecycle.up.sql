ALTER TABLE recovery_points
    ADD COLUMN point_revision INTEGER NOT NULL DEFAULT 1 CHECK (point_revision > 0);

CREATE TRIGGER trg_recovery_points_point_revision_guard
BEFORE UPDATE ON recovery_points
WHEN NEW.point_revision IS NOT OLD.point_revision
    AND NEW.point_revision IS NOT OLD.point_revision + 1
BEGIN
    SELECT RAISE(ABORT, 'recovery point revision must advance exactly once');
END;

CREATE TRIGGER trg_recovery_points_point_revision_advance
AFTER UPDATE ON recovery_points
WHEN NEW.point_revision IS OLD.point_revision
BEGIN
    UPDATE recovery_points
    SET point_revision = OLD.point_revision + 1
    WHERE id = NEW.id AND point_revision = OLD.point_revision;
END;

-- SQLite cannot rebuild this parent table while 000066-000069 rows reference it
-- inside golang-migrate's transaction. Change only its closed CHECK definition,
-- then force SQLite to reparse the schema before later statements use it.
PRAGMA writable_schema = ON;
UPDATE sqlite_schema
SET sql = replace(sql, '''search_index''))', '''search_index'', ''retention_worker''))')
WHERE type = 'table'
  AND name = 'recovery_point_leases'
  AND sql LIKE '%''search_index''))%'
  AND sql NOT LIKE '%''retention_worker''%';
PRAGMA writable_schema = RESET;

CREATE TABLE backup_retention_policies (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('repository', 'task_link')),
    scope_id TEXT NOT NULL CHECK (length(scope_id) = 32 AND scope_id NOT GLOB '*[^0-9a-f]*'),
    revision INTEGER NOT NULL CHECK (revision > 0),
    rules_json TEXT NOT NULL CHECK (length(rules_json) > 0 AND json_valid(rules_json)),
    status TEXT NOT NULL CHECK (status IN ('active', 'deleted')),
    created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME,
    CHECK ((status = 'active' AND deleted_at IS NULL)
        OR (status = 'deleted' AND deleted_at IS NOT NULL))
);
CREATE UNIQUE INDEX idx_backup_retention_policies_active_scope
    ON backup_retention_policies(scope_kind, scope_id)
    WHERE status = 'active';

CREATE TABLE recovery_point_holds (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32 AND recovery_point_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    hold_type TEXT NOT NULL CHECK (hold_type IN ('operational', 'legal')),
    state TEXT NOT NULL CHECK (state IN ('active', 'released')),
    encrypted_reason TEXT NOT NULL CHECK (length(encrypted_reason) > 0),
    created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    expires_at DATETIME,
    released_by INTEGER REFERENCES users(id) ON DELETE RESTRICT,
    released_at DATETIME,
    encrypted_release_reason TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((state = 'active' AND released_by IS NULL AND released_at IS NULL AND encrypted_release_reason = '')
        OR (state = 'released' AND released_by IS NOT NULL AND released_at IS NOT NULL
            AND encrypted_release_reason <> '' AND released_at >= created_at))
);
CREATE UNIQUE INDEX idx_recovery_point_holds_active_type
    ON recovery_point_holds(recovery_point_id, hold_type)
    WHERE state = 'active';
CREATE INDEX idx_recovery_point_holds_expiry
    ON recovery_point_holds(state, expires_at, recovery_point_id);
CREATE TRIGGER trg_recovery_point_holds_release_one_way
BEFORE UPDATE ON recovery_point_holds
WHEN OLD.state = 'released'
    OR NEW.id IS NOT OLD.id
    OR NEW.recovery_point_id IS NOT OLD.recovery_point_id
    OR NEW.hold_type IS NOT OLD.hold_type
    OR NEW.encrypted_reason IS NOT OLD.encrypted_reason
    OR NEW.created_by IS NOT OLD.created_by
    OR NEW.created_at IS NOT OLD.created_at
    OR NEW.expires_at IS NOT OLD.expires_at
    OR (OLD.state = 'active' AND NEW.state NOT IN ('active', 'released'))
BEGIN
    SELECT RAISE(ABORT, 'recovery point hold release is one-way');
END;

CREATE TABLE backup_asset_purge_plans (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    repository_id TEXT NOT NULL CHECK (length(repository_id) = 32 AND repository_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    requester_id INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    impact_revision INTEGER NOT NULL CHECK (impact_revision > 0),
    expires_at DATETIME NOT NULL,
    hold_count INTEGER NOT NULL DEFAULT 0 CHECK (hold_count >= 0),
    lease_count INTEGER NOT NULL DEFAULT 0 CHECK (lease_count >= 0),
    worm_count INTEGER NOT NULL DEFAULT 0 CHECK (worm_count >= 0),
    status TEXT NOT NULL CHECK (status IN ('ready', 'bound', 'executing', 'consumed', 'invalidated')),
    execute_actor_id INTEGER REFERENCES users(id) ON DELETE RESTRICT,
    execute_proof_digest TEXT NOT NULL DEFAULT '' CHECK (execute_proof_digest = ''
        OR (length(execute_proof_digest) = 64 AND execute_proof_digest NOT GLOB '*[^0-9a-f]*')),
    execute_reason_digest TEXT NOT NULL DEFAULT '' CHECK (execute_reason_digest = ''
        OR (length(execute_reason_digest) = 64 AND execute_reason_digest NOT GLOB '*[^0-9a-f]*')),
    execute_bound_at DATETIME,
    consumed_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (id, revision),
    CHECK (expires_at > created_at),
    CHECK (
        (status = 'ready' AND execute_actor_id IS NULL AND execute_proof_digest = ''
            AND execute_reason_digest = '' AND execute_bound_at IS NULL AND consumed_at IS NULL)
        OR (status IN ('bound', 'executing') AND execute_actor_id IS NOT NULL
            AND length(execute_proof_digest) = 64 AND length(execute_reason_digest) = 64
            AND execute_bound_at IS NOT NULL AND consumed_at IS NULL)
        OR (status = 'consumed' AND execute_actor_id IS NOT NULL
            AND length(execute_proof_digest) = 64 AND length(execute_reason_digest) = 64
            AND execute_bound_at IS NOT NULL AND consumed_at IS NOT NULL AND consumed_at >= execute_bound_at)
        OR (status = 'invalidated' AND consumed_at IS NULL
            AND ((execute_actor_id IS NULL AND execute_proof_digest = '' AND execute_reason_digest = '' AND execute_bound_at IS NULL)
                OR (execute_actor_id IS NOT NULL AND length(execute_proof_digest) = 64
                    AND length(execute_reason_digest) = 64 AND execute_bound_at IS NOT NULL)))
    )
);
CREATE INDEX idx_backup_asset_purge_plans_repository_status
    ON backup_asset_purge_plans(repository_id, status, expires_at);

CREATE TABLE backup_asset_purge_plan_items (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plan_id TEXT NOT NULL CHECK (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES backup_asset_purge_plans(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32 AND recovery_point_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    expected_point_revision INTEGER NOT NULL CHECK (expected_point_revision > 0),
    expected_capability_revision INTEGER NOT NULL CHECK (expected_capability_revision > 0),
    created_at DATETIME NOT NULL,
    UNIQUE (plan_id, recovery_point_id)
);
CREATE UNIQUE INDEX idx_backup_asset_purge_plan_items_plan_ordinal
    ON backup_asset_purge_plan_items(plan_id, ordinal);

CREATE TABLE recovery_point_lifecycle_attempts (
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
CREATE UNIQUE INDEX idx_recovery_point_lifecycle_attempts_active
    ON recovery_point_lifecycle_attempts(recovery_point_id)
    WHERE phase <> 'complete';
CREATE INDEX idx_recovery_point_lifecycle_attempts_retry
    ON recovery_point_lifecycle_attempts(phase, retry_at, updated_at);

CREATE TABLE recovery_point_lifecycle_tombstones (
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32 AND recovery_point_id NOT GLOB '*[^0-9a-f]*'),
    repository_id TEXT NOT NULL CHECK (length(repository_id) = 32 AND repository_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    original_semantics TEXT NOT NULL CHECK (original_semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline', 'mutable_head')),
    terminal_operation TEXT NOT NULL CHECK (terminal_operation IN ('retention_expire', 'explicit_purge', 'mutable_retire')),
    terminal_state TEXT NOT NULL CHECK (terminal_state IN ('retired', 'expired')),
    managed_history INTEGER NOT NULL DEFAULT 1 CHECK (managed_history = 1),
    deletion_receipt_digest TEXT CHECK (deletion_receipt_digest IS NULL OR (length(deletion_receipt_digest) = 64 AND deletion_receipt_digest NOT GLOB '*[^0-9a-f]*')),
    retired_at DATETIME,
    purged_at DATETIME,
    result_code TEXT NOT NULL CHECK (result_code IN ('mutable_retired', 'provider_deleted', 'provider_already_absent')),
    created_at DATETIME NOT NULL,
    PRIMARY KEY (recovery_point_id, terminal_operation),
    CHECK ((terminal_operation = 'mutable_retire' AND original_semantics = 'mutable_head'
            AND terminal_state = 'retired' AND retired_at IS NOT NULL
            AND purged_at IS NULL AND deletion_receipt_digest IS NULL AND result_code = 'mutable_retired')
        OR (terminal_operation IN ('retention_expire', 'explicit_purge') AND terminal_state = 'expired'
            AND retired_at IS NULL AND purged_at IS NOT NULL AND deletion_receipt_digest IS NOT NULL
            AND result_code IN ('provider_deleted', 'provider_already_absent')))
);
CREATE INDEX idx_recovery_point_lifecycle_tombstones_repository
    ON recovery_point_lifecycle_tombstones(repository_id, created_at);
CREATE TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_update
BEFORE UPDATE ON recovery_point_lifecycle_tombstones
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle tombstone is immutable');
END;
CREATE TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_delete
BEFORE DELETE ON recovery_point_lifecycle_tombstones
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle tombstone is permanent');
END;

CREATE TABLE backup_repository_import_candidates (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    repository_id TEXT NOT NULL CHECK (length(repository_id) = 32 AND repository_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    candidate_kind TEXT NOT NULL CHECK (candidate_kind IN ('native_snapshot', 'xirang_manifest', 'imported_baseline', 'mutable_head')),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) = 64 AND source_fingerprint NOT GLOB '*[^0-9a-f]*'),
    encrypted_provider_locator TEXT NOT NULL CHECK (length(encrypted_provider_locator) > 0),
    encrypted_evidence TEXT NOT NULL CHECK (length(encrypted_evidence) > 0),
    review_state TEXT NOT NULL CHECK (review_state IN ('pending', 'accepted', 'rejected')),
    reviewed_by INTEGER REFERENCES users(id) ON DELETE RESTRICT,
    reviewed_at DATETIME,
    accepted_recovery_point_id TEXT REFERENCES recovery_points(id) ON DELETE RESTRICT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((review_state = 'pending' AND reviewed_by IS NULL AND reviewed_at IS NULL AND accepted_recovery_point_id IS NULL)
        OR (review_state = 'accepted' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL AND accepted_recovery_point_id IS NOT NULL)
        OR (review_state = 'rejected' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL AND accepted_recovery_point_id IS NULL))
);
CREATE UNIQUE INDEX idx_backup_repository_import_candidates_source
    ON backup_repository_import_candidates(repository_id, source_fingerprint);
CREATE INDEX idx_backup_repository_import_candidates_review
    ON backup_repository_import_candidates(repository_id, review_state, created_at);

CREATE TABLE backup_asset_config_import_refs (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    source_document_id TEXT NOT NULL CHECK (length(source_document_id) = 32 AND source_document_id NOT GLOB '*[^0-9a-f]*'),
    source_reference TEXT NOT NULL CHECK (length(source_reference) BETWEEN 1 AND 128 AND source_reference GLOB '*[^0-9]*'),
    entity_kind TEXT NOT NULL CHECK (entity_kind IN ('repository', 'task_link', 'retention_policy', 'hold')),
    local_entity_id TEXT NOT NULL CHECK (length(local_entity_id) = 32 AND local_entity_id NOT GLOB '*[^0-9a-f]*'),
    created_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX idx_backup_asset_config_import_refs_source
    ON backup_asset_config_import_refs(source_document_id, source_reference, entity_kind);
CREATE UNIQUE INDEX idx_backup_asset_config_import_refs_local
    ON backup_asset_config_import_refs(source_document_id, entity_kind, local_entity_id);

-- golang-migrate writes the target version dirty before running a down body.
-- Reject used state at that metadata boundary so version 70 remains clean.
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
