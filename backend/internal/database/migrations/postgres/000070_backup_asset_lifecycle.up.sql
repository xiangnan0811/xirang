BEGIN;

ALTER TABLE recovery_points
    ADD COLUMN point_revision BIGINT NOT NULL DEFAULT 1 CHECK (point_revision > 0);

CREATE FUNCTION recovery_point_revision_advance()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.point_revision = OLD.point_revision THEN
        NEW.point_revision := OLD.point_revision + 1;
    ELSIF NEW.point_revision IS DISTINCT FROM OLD.point_revision + 1 THEN
        RAISE EXCEPTION 'recovery point revision must advance exactly once';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_recovery_points_point_revision
BEFORE UPDATE ON recovery_points
FOR EACH ROW EXECUTE FUNCTION recovery_point_revision_advance();

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication', 'search_index', 'retention_worker'));

CREATE TABLE backup_retention_policies (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    scope_kind VARCHAR(16) NOT NULL CHECK (scope_kind IN ('repository', 'task_link')),
    scope_id VARCHAR(32) NOT NULL CHECK (scope_id ~ '^[0-9a-f]{32}$'),
    revision BIGINT NOT NULL CHECK (revision > 0),
    rules_json TEXT NOT NULL CHECK (length(rules_json) > 0 AND rules_json::jsonb IS NOT NULL),
    status VARCHAR(16) NOT NULL CHECK (status IN ('active', 'deleted')),
    created_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ,
    CHECK ((status = 'active' AND deleted_at IS NULL)
        OR (status = 'deleted' AND deleted_at IS NOT NULL))
);
CREATE UNIQUE INDEX idx_backup_retention_policies_active_scope
    ON backup_retention_policies(scope_kind, scope_id)
    WHERE status = 'active';

CREATE TABLE recovery_point_holds (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    hold_type VARCHAR(16) NOT NULL CHECK (hold_type IN ('operational', 'legal')),
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'released')),
    encrypted_reason TEXT NOT NULL CHECK (length(encrypted_reason) > 0),
    created_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    expires_at TIMESTAMPTZ,
    released_by BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    released_at TIMESTAMPTZ,
    encrypted_release_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK ((state = 'active' AND released_by IS NULL AND released_at IS NULL AND encrypted_release_reason = '')
        OR (state = 'released' AND released_by IS NOT NULL AND released_at IS NOT NULL
            AND encrypted_release_reason <> '' AND released_at >= created_at))
);
CREATE UNIQUE INDEX idx_recovery_point_holds_active_type
    ON recovery_point_holds(recovery_point_id, hold_type)
    WHERE state = 'active';
CREATE INDEX idx_recovery_point_holds_expiry
    ON recovery_point_holds(state, expires_at, recovery_point_id);

CREATE OR REPLACE FUNCTION recovery_point_hold_release_one_way()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state = 'released'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.recovery_point_id IS DISTINCT FROM OLD.recovery_point_id
       OR NEW.hold_type IS DISTINCT FROM OLD.hold_type
       OR NEW.encrypted_reason IS DISTINCT FROM OLD.encrypted_reason
       OR NEW.created_by IS DISTINCT FROM OLD.created_by
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
       OR (OLD.state = 'active' AND NEW.state NOT IN ('active', 'released')) THEN
        RAISE EXCEPTION 'recovery point hold release is one-way';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_recovery_point_holds_release_one_way
BEFORE UPDATE ON recovery_point_holds
FOR EACH ROW EXECUTE FUNCTION recovery_point_hold_release_one_way();

CREATE TABLE backup_asset_purge_plans (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    repository_id VARCHAR(32) NOT NULL CHECK (repository_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    requester_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    impact_revision BIGINT NOT NULL CHECK (impact_revision > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    hold_count BIGINT NOT NULL DEFAULT 0 CHECK (hold_count >= 0),
    lease_count BIGINT NOT NULL DEFAULT 0 CHECK (lease_count >= 0),
    worm_count BIGINT NOT NULL DEFAULT 0 CHECK (worm_count >= 0),
    status VARCHAR(16) NOT NULL CHECK (status IN ('ready', 'bound', 'executing', 'consumed', 'invalidated')),
    execute_actor_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    execute_proof_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (execute_proof_digest = '' OR execute_proof_digest ~ '^[0-9a-f]{64}$'),
    execute_reason_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (execute_reason_digest = '' OR execute_reason_digest ~ '^[0-9a-f]{64}$'),
    execute_bound_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, revision),
    CHECK (expires_at > created_at),
    CHECK (
        (status = 'ready' AND execute_actor_id IS NULL AND execute_proof_digest = ''
            AND execute_reason_digest = '' AND execute_bound_at IS NULL AND consumed_at IS NULL)
        OR (status IN ('bound', 'executing') AND execute_actor_id IS NOT NULL
            AND execute_proof_digest ~ '^[0-9a-f]{64}$' AND execute_reason_digest ~ '^[0-9a-f]{64}$'
            AND execute_bound_at IS NOT NULL AND consumed_at IS NULL)
        OR (status = 'consumed' AND execute_actor_id IS NOT NULL
            AND execute_proof_digest ~ '^[0-9a-f]{64}$' AND execute_reason_digest ~ '^[0-9a-f]{64}$'
            AND execute_bound_at IS NOT NULL AND consumed_at IS NOT NULL AND consumed_at >= execute_bound_at)
        OR (status = 'invalidated' AND consumed_at IS NULL
            AND ((execute_actor_id IS NULL AND execute_proof_digest = '' AND execute_reason_digest = '' AND execute_bound_at IS NULL)
                OR (execute_actor_id IS NOT NULL AND execute_proof_digest ~ '^[0-9a-f]{64}$'
                    AND execute_reason_digest ~ '^[0-9a-f]{64}$' AND execute_bound_at IS NOT NULL)))
    )
);
CREATE INDEX idx_backup_asset_purge_plans_repository_status
    ON backup_asset_purge_plans(repository_id, status, expires_at);

CREATE TABLE backup_asset_purge_plan_items (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plan_id VARCHAR(32) NOT NULL CHECK (plan_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_purge_plans(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    expected_point_revision BIGINT NOT NULL CHECK (expected_point_revision > 0),
    expected_capability_revision INTEGER NOT NULL CHECK (expected_capability_revision > 0),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (plan_id, recovery_point_id)
);
CREATE UNIQUE INDEX idx_backup_asset_purge_plan_items_plan_ordinal
    ON backup_asset_purge_plan_items(plan_id, ordinal);

CREATE TABLE recovery_point_lifecycle_attempts (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    operation VARCHAR(24) NOT NULL CHECK (operation IN ('retention_expire', 'explicit_purge', 'mutable_retire')),
    phase VARCHAR(24) NOT NULL CHECK (phase IN ('selected', 'revoking', 'draining', 'cleaning', 'provider_delete', 'tombstoning', 'blocked', 'complete')),
    transition_revision BIGINT NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    policy_id VARCHAR(32) CHECK (policy_id IS NULL OR policy_id ~ '^[0-9a-f]{32}$'),
    policy_revision BIGINT CHECK (policy_revision IS NULL OR policy_revision > 0),
    policy_rule_digest VARCHAR(64) CHECK (policy_rule_digest IS NULL OR policy_rule_digest ~ '^[0-9a-f]{64}$'),
    evaluation_time TIMESTAMPTZ,
    purge_plan_id VARCHAR(32) CHECK (purge_plan_id IS NULL OR purge_plan_id ~ '^[0-9a-f]{32}$'),
    purge_plan_revision BIGINT CHECK (purge_plan_revision IS NULL OR purge_plan_revision > 0),
    purge_actor_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    lease_id VARCHAR(32) CHECK (lease_id IS NULL OR lease_id ~ '^[0-9a-f]{32}$'),
    lease_attempt_id VARCHAR(32) CHECK (lease_attempt_id IS NULL OR lease_attempt_id ~ '^[0-9a-f]{32}$'),
    lease_fence_token_hash VARCHAR(64) CHECK (lease_fence_token_hash IS NULL OR lease_fence_token_hash ~ '^[0-9a-f]{64}$'),
    blocked_reason VARCHAR(48) NOT NULL DEFAULT '' CHECK (blocked_reason IN ('', 'active_hold', 'lease_live', 'lease_drain_unproven', 'owner_cleanup_unproven', 'provider_worm', 'provider_unavailable', 'provider_identity_conflict', 'provider_delete_unproven', 'deletion_unavailable', 'fence_lost')),
    claimed_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    retry_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
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
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$'),
    repository_id VARCHAR(32) NOT NULL CHECK (repository_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    original_semantics VARCHAR(32) NOT NULL CHECK (original_semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline', 'mutable_head')),
    terminal_operation VARCHAR(24) NOT NULL CHECK (terminal_operation IN ('retention_expire', 'explicit_purge', 'mutable_retire')),
    terminal_state VARCHAR(16) NOT NULL CHECK (terminal_state IN ('retired', 'expired')),
    managed_history BOOLEAN NOT NULL DEFAULT TRUE CHECK (managed_history),
    deletion_receipt_digest VARCHAR(64) CHECK (deletion_receipt_digest IS NULL OR deletion_receipt_digest ~ '^[0-9a-f]{64}$'),
    retired_at TIMESTAMPTZ,
    purged_at TIMESTAMPTZ,
    result_code VARCHAR(32) NOT NULL CHECK (result_code IN ('mutable_retired', 'provider_deleted', 'provider_already_absent')),
    created_at TIMESTAMPTZ NOT NULL,
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

CREATE OR REPLACE FUNCTION recovery_point_lifecycle_tombstone_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'recovery point lifecycle tombstone is permanent';
END;
$$;
CREATE TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_update
BEFORE UPDATE ON recovery_point_lifecycle_tombstones
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_tombstone_immutable();
CREATE TRIGGER trg_recovery_point_lifecycle_tombstones_immutable_delete
BEFORE DELETE ON recovery_point_lifecycle_tombstones
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_tombstone_immutable();

CREATE TABLE backup_repository_import_candidates (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    repository_id VARCHAR(32) NOT NULL CHECK (repository_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    candidate_kind VARCHAR(24) NOT NULL CHECK (candidate_kind IN ('native_snapshot', 'xirang_manifest', 'imported_baseline', 'mutable_head')),
    source_fingerprint VARCHAR(64) NOT NULL CHECK (source_fingerprint ~ '^[0-9a-f]{64}$'),
    encrypted_provider_locator TEXT NOT NULL CHECK (length(encrypted_provider_locator) > 0),
    encrypted_evidence TEXT NOT NULL CHECK (length(encrypted_evidence) > 0),
    review_state VARCHAR(16) NOT NULL CHECK (review_state IN ('pending', 'accepted', 'rejected')),
    reviewed_by BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    reviewed_at TIMESTAMPTZ,
    accepted_recovery_point_id VARCHAR(32) REFERENCES recovery_points(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK ((review_state = 'pending' AND reviewed_by IS NULL AND reviewed_at IS NULL AND accepted_recovery_point_id IS NULL)
        OR (review_state = 'accepted' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL AND accepted_recovery_point_id IS NOT NULL)
        OR (review_state = 'rejected' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL AND accepted_recovery_point_id IS NULL))
);
CREATE UNIQUE INDEX idx_backup_repository_import_candidates_source
    ON backup_repository_import_candidates(repository_id, source_fingerprint);
CREATE INDEX idx_backup_repository_import_candidates_review
    ON backup_repository_import_candidates(repository_id, review_state, created_at);

CREATE TABLE backup_asset_config_import_refs (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    source_document_id VARCHAR(32) NOT NULL CHECK (source_document_id ~ '^[0-9a-f]{32}$'),
    source_reference VARCHAR(128) NOT NULL CHECK (length(source_reference) BETWEEN 1 AND 128 AND source_reference !~ '^[0-9]+$'),
    entity_kind VARCHAR(24) NOT NULL CHECK (entity_kind IN ('repository', 'task_link', 'retention_policy', 'hold')),
    local_entity_id VARCHAR(32) NOT NULL CHECK (local_entity_id ~ '^[0-9a-f]{32}$'),
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_backup_asset_config_import_refs_source
    ON backup_asset_config_import_refs(source_document_id, source_reference, entity_kind);
CREATE UNIQUE INDEX idx_backup_asset_config_import_refs_local
    ON backup_asset_config_import_refs(source_document_id, entity_kind, local_entity_id);

CREATE OR REPLACE FUNCTION backup_asset_lifecycle_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 70 AND (
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
    ) THEN
        RAISE EXCEPTION '000070 downgrade blocked: backup asset lifecycle state exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_lifecycle_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION backup_asset_lifecycle_downgrade_admission();

COMMIT;
