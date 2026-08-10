BEGIN;

ALTER TABLE task_runs ADD COLUMN node_id_snapshot BIGINT;
UPDATE task_runs AS run
SET node_id_snapshot = task_row.node_id
FROM tasks AS task_row
WHERE task_row.id = run.task_id;
ALTER TABLE task_runs
    ALTER COLUMN node_id_snapshot SET NOT NULL,
    ADD CONSTRAINT task_runs_node_id_snapshot_positive CHECK (node_id_snapshot > 0);
CREATE INDEX idx_task_runs_node_snapshot_status
    ON task_runs(node_id_snapshot, status);

CREATE OR REPLACE FUNCTION backup_asset_recovery_task_run_node_snapshot_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    task_node_id BIGINT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.task_id IS DISTINCT FROM OLD.task_id
           OR NEW.node_id_snapshot IS DISTINCT FROM OLD.node_id_snapshot THEN
            RAISE EXCEPTION 'TaskRun task and node snapshot are immutable';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.node_id_snapshot IS NULL OR NEW.node_id_snapshot <= 0 THEN
        RAISE EXCEPTION 'TaskRun node snapshot must be positive';
    END IF;
    SELECT node_id INTO task_node_id
    FROM tasks
    WHERE id = NEW.task_id
    FOR SHARE;
    IF NOT FOUND OR task_node_id IS DISTINCT FROM NEW.node_id_snapshot THEN
        RAISE EXCEPTION 'TaskRun node snapshot must match the Task node at creation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_insert
BEFORE INSERT ON task_runs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_task_run_node_snapshot_guard();
CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_immutable
BEFORE UPDATE OF task_id, node_id_snapshot ON task_runs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_task_run_node_snapshot_guard();

CREATE UNIQUE INDEX idx_recovery_points_repository_id_id
    ON recovery_points(repository_id, id);

CREATE TABLE backup_asset_recovery_plans (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    requester_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    endpoint VARCHAR(64) NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 64),
    idempotency_key_digest VARCHAR(64) NOT NULL CHECK (idempotency_key_digest ~ '^[0-9a-f]{64}$'),
    repository_id VARCHAR(32) NOT NULL CHECK (repository_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$')
        REFERENCES recovery_points(id) ON DELETE RESTRICT,
    source_revision_digest VARCHAR(64) NOT NULL CHECK (source_revision_digest ~ '^[0-9a-f]{64}$'),
    source_revision_kind VARCHAR(16) NOT NULL CHECK (source_revision_kind IN ('immutable', 'observation')),
    immutable_locator_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (immutable_locator_digest = ''
        OR immutable_locator_digest ~ '^[0-9a-f]{64}$'),
    immutable_manifest_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (immutable_manifest_digest = ''
        OR immutable_manifest_digest ~ '^[0-9a-f]{64}$'),
    observation_fingerprint VARCHAR(64) NOT NULL DEFAULT '' CHECK (observation_fingerprint = ''
        OR observation_fingerprint ~ '^[0-9a-f]{64}$'),
    catalog_generation_id VARCHAR(32) NOT NULL CHECK (catalog_generation_id ~ '^[0-9a-f]{32}$'),
    observed_at TIMESTAMPTZ,
    encrypted_source_locator TEXT NOT NULL DEFAULT '',
    target_mode VARCHAR(16) NOT NULL CHECK (target_mode IN ('isolated', 'in_place')),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    target_root_id VARCHAR(32) NOT NULL CHECK (length(target_root_id) BETWEEN 1 AND 32),
    encrypted_target_root_locator TEXT NOT NULL DEFAULT '',
    encrypted_target_relative_path TEXT NOT NULL DEFAULT '',
    root_locator_digest VARCHAR(64) NOT NULL CHECK (root_locator_digest ~ '^[0-9a-f]{64}$'),
    path_digest VARCHAR(64) NOT NULL CHECK (path_digest ~ '^[0-9a-f]{64}$'),
    target_base_revision VARCHAR(64) NOT NULL CHECK (length(target_base_revision) BETWEEN 1 AND 64),
    credential_scope_revision VARCHAR(64) NOT NULL CHECK (length(credential_scope_revision) BETWEEN 1 AND 64),
    root_revision VARCHAR(64) NOT NULL CHECK (length(root_revision) BETWEEN 1 AND 64),
    filesystem_revision VARCHAR(64) NOT NULL CHECK (length(filesystem_revision) BETWEEN 1 AND 64),
    selection_digest VARCHAR(64) NOT NULL CHECK (selection_digest ~ '^[0-9a-f]{64}$'),
    binding_digest VARCHAR(64) NOT NULL CHECK (binding_digest ~ '^[0-9a-f]{64}$'),
    capability_revision VARCHAR(64) NOT NULL CHECK (length(capability_revision) BETWEEN 1 AND 64),
    conflict_policy VARCHAR(32) NOT NULL CHECK (conflict_policy IN ('fail_on_conflict', 'skip_existing', 'overwrite_selected', 'exact_mirror')),
    operation_set_digest VARCHAR(64) NOT NULL CHECK (operation_set_digest ~ '^[0-9a-f]{64}$'),
    delete_set_digest VARCHAR(64) NOT NULL CHECK (delete_set_digest ~ '^[0-9a-f]{64}$'),
    security_decision VARCHAR(32) NOT NULL CHECK (security_decision IN ('allow_clean', 'block', 'admin_override')),
    security_decision_digest VARCHAR(64) NOT NULL CHECK (security_decision_digest ~ '^[0-9a-f]{64}$'),
    security_finding_set_digest VARCHAR(64) NOT NULL CHECK (security_finding_set_digest ~ '^[0-9a-f]{64}$'),
    security_policy_revision VARCHAR(64) NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 64),
    security_override_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (security_override_binding_digest = ''
        OR security_override_binding_digest ~ '^[0-9a-f]{64}$'),
    encrypted_override_reason TEXT NOT NULL DEFAULT '',
    preflight_revision VARCHAR(64) NOT NULL CHECK (length(preflight_revision) BETWEEN 1 AND 64),
    preflight_expires_at TIMESTAMPTZ NOT NULL,
    estimated_items BIGINT NOT NULL DEFAULT 0 CHECK (estimated_items >= 0),
    estimated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    state VARCHAR(32) NOT NULL CHECK (state IN ('draft', 'preflight_ready', 'authorized', 'executed', 'canceled', 'superseded', 'expired')),
    transition_revision BIGINT NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (requester_id, endpoint, idempotency_key_digest),
    CHECK (
        (source_revision_kind = 'immutable'
            AND length(immutable_locator_digest) = 64 AND length(immutable_manifest_digest) = 64
            AND observation_fingerprint = '' AND observed_at IS NULL)
        OR (source_revision_kind = 'observation'
            AND immutable_locator_digest = '' AND immutable_manifest_digest = ''
            AND length(observation_fingerprint) = 64 AND observed_at IS NOT NULL
            AND observed_at <= created_at AND observed_at < preflight_expires_at)
    ),
    CHECK (
        (conflict_policy = 'exact_mirror' AND target_mode = 'in_place')
        OR (conflict_policy <> 'exact_mirror'
            AND delete_set_digest = '3f5a5d5213612b170da6ce2f2f90775a31d4e40269bb785042589af64011b7cf')
    ),
    CHECK (
        (security_decision = 'admin_override'
            AND length(security_override_binding_digest) = 64 AND encrypted_override_reason <> '')
        OR (security_decision IN ('allow_clean', 'block')
            AND security_override_binding_digest = '' AND encrypted_override_reason = '')
    ),
    CHECK (preflight_expires_at > created_at AND created_at <= updated_at),
    FOREIGN KEY (repository_id, recovery_point_id)
        REFERENCES recovery_points(repository_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_plans_id_generation_point
    ON backup_asset_recovery_plans(id, catalog_generation_id, recovery_point_id);
CREATE UNIQUE INDEX idx_backup_asset_recovery_plans_id_target
    ON backup_asset_recovery_plans(id, target_mode, target_node_id);
CREATE UNIQUE INDEX idx_backup_asset_recovery_plans_id_target_binding
    ON backup_asset_recovery_plans(
        id, target_node_id, target_root_id, root_locator_digest, path_digest, target_base_revision
    );
CREATE INDEX idx_backup_asset_recovery_plans_requester_state
    ON backup_asset_recovery_plans(requester_id, state, updated_at);
CREATE INDEX idx_backup_asset_recovery_plans_preflight_expiry
    ON backup_asset_recovery_plans(state, preflight_expires_at);

CREATE TABLE backup_asset_recovery_plan_items (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plan_id VARCHAR(32) NOT NULL CHECK (plan_id ~ '^[0-9a-f]{32}$'),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$'),
    catalog_generation_id VARCHAR(32) NOT NULL CHECK (catalog_generation_id ~ '^[0-9a-f]{32}$'),
    entry_id VARCHAR(64) NOT NULL CHECK (entry_id ~ '^[0-9a-f]{64}$'),
    entry_type VARCHAR(16) NOT NULL CHECK (entry_type IN ('file', 'directory', 'symlink', 'hardlink', 'special', 'unknown')),
    source_fingerprint VARCHAR(64) NOT NULL DEFAULT '' CHECK (source_fingerprint = ''
        OR source_fingerprint ~ '^[0-9a-f]{64}$'),
    relative_path_digest VARCHAR(64) NOT NULL CHECK (relative_path_digest ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (plan_id, ordinal),
    UNIQUE (id, plan_id, ordinal),
    UNIQUE (id, plan_id),
    UNIQUE (plan_id, recovery_point_id, entry_id),
    FOREIGN KEY (plan_id, catalog_generation_id, recovery_point_id)
        REFERENCES backup_asset_recovery_plans(id, catalog_generation_id, recovery_point_id) ON DELETE CASCADE,
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_recovery_plan_items_plan_ordinal
    ON backup_asset_recovery_plan_items(plan_id, ordinal);

CREATE TABLE backup_asset_recovery_preflights (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plan_id VARCHAR(32) NOT NULL CHECK (plan_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_plans(id) ON DELETE CASCADE,
    revision VARCHAR(64) NOT NULL CHECK (length(revision) BETWEEN 1 AND 64),
    source_revision_digest VARCHAR(64) NOT NULL CHECK (source_revision_digest ~ '^[0-9a-f]{64}$'),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    node_revision VARCHAR(64) NOT NULL CHECK (length(node_revision) BETWEEN 1 AND 64),
    target_root_id VARCHAR(32) NOT NULL CHECK (length(target_root_id) BETWEEN 1 AND 32),
    root_locator_digest VARCHAR(64) NOT NULL CHECK (root_locator_digest ~ '^[0-9a-f]{64}$'),
    path_digest VARCHAR(64) NOT NULL CHECK (path_digest ~ '^[0-9a-f]{64}$'),
    target_revision VARCHAR(64) NOT NULL CHECK (length(target_revision) BETWEEN 1 AND 64),
    capability_revision VARCHAR(64) NOT NULL CHECK (length(capability_revision) BETWEEN 1 AND 64),
    policy_revision VARCHAR(64) NOT NULL CHECK (length(policy_revision) BETWEEN 1 AND 64),
    finding_set_digest VARCHAR(64) NOT NULL CHECK (finding_set_digest ~ '^[0-9a-f]{64}$'),
    security_override_candidate_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (security_override_candidate_digest = ''
        OR security_override_candidate_digest ~ '^[0-9a-f]{64}$'),
    security_override_categories VARCHAR(96) NOT NULL DEFAULT '' CHECK (security_override_categories IN (
        '', 'malware', 'suspicious', 'test_signature', 'malware,suspicious',
        'malware,test_signature', 'suspicious,test_signature', 'malware,suspicious,test_signature'
    )),
    operation_set_digest VARCHAR(64) NOT NULL CHECK (operation_set_digest ~ '^[0-9a-f]{64}$'),
    delete_set_digest VARCHAR(64) NOT NULL CHECK (delete_set_digest ~ '^[0-9a-f]{64}$'),
    encrypted_operation_rows TEXT NOT NULL CHECK (length(encrypted_operation_rows) > 0),
    estimated_items BIGINT NOT NULL DEFAULT 0 CHECK (estimated_items >= 0),
    estimated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, plan_id),
    UNIQUE (id, plan_id, target_node_id, target_root_id, root_locator_digest, path_digest, node_revision),
    UNIQUE (plan_id, revision),
    CHECK (expires_at > created_at),
    CHECK (
        (security_override_candidate_digest = '' AND security_override_categories = '')
        OR (security_override_candidate_digest ~ '^[0-9a-f]{64}$' AND security_override_categories <> '')
    ),
    FOREIGN KEY (plan_id, target_node_id, target_root_id, root_locator_digest, path_digest, node_revision)
        REFERENCES backup_asset_recovery_plans(
            id, target_node_id, target_root_id, root_locator_digest, path_digest, target_base_revision
        ) ON DELETE CASCADE
);
CREATE INDEX idx_backup_asset_recovery_preflights_plan_expiry
    ON backup_asset_recovery_preflights(plan_id, expires_at DESC);

CREATE TABLE backup_asset_recovery_jobs (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plan_id VARCHAR(32) NOT NULL CHECK (plan_id ~ '^[0-9a-f]{32}$'),
    plan_binding_digest VARCHAR(64) NOT NULL CHECK (plan_binding_digest ~ '^[0-9a-f]{64}$'),
    selection_digest VARCHAR(64) NOT NULL CHECK (selection_digest ~ '^[0-9a-f]{64}$'),
    source_revision_digest VARCHAR(64) NOT NULL CHECK (source_revision_digest ~ '^[0-9a-f]{64}$'),
    preflight_id VARCHAR(32) NOT NULL CHECK (preflight_id ~ '^[0-9a-f]{32}$'),
    preflight_revision VARCHAR(64) NOT NULL CHECK (length(preflight_revision) BETWEEN 1 AND 64),
    preflight_expires_at TIMESTAMPTZ NOT NULL,
    preflight_target_revision VARCHAR(64) NOT NULL CHECK (length(preflight_target_revision) BETWEEN 1 AND 64),
    preflight_node_revision VARCHAR(64) NOT NULL CHECK (length(preflight_node_revision) BETWEEN 1 AND 64),
    capability_revision VARCHAR(64) NOT NULL CHECK (length(capability_revision) BETWEEN 1 AND 64),
    operation_set_digest VARCHAR(64) NOT NULL CHECK (operation_set_digest ~ '^[0-9a-f]{64}$'),
    delete_set_digest VARCHAR(64) NOT NULL CHECK (delete_set_digest ~ '^[0-9a-f]{64}$'),
    security_decision VARCHAR(32) NOT NULL CHECK (security_decision IN ('allow_clean', 'admin_override')),
    security_decision_digest VARCHAR(64) NOT NULL CHECK (security_decision_digest ~ '^[0-9a-f]{64}$'),
    security_finding_set_digest VARCHAR(64) NOT NULL CHECK (security_finding_set_digest ~ '^[0-9a-f]{64}$'),
    security_policy_revision VARCHAR(64) NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 64),
    security_override_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (security_override_binding_digest = ''
        OR security_override_binding_digest ~ '^[0-9a-f]{64}$'),
    estimated_items BIGINT NOT NULL DEFAULT 0 CHECK (estimated_items >= 0),
    estimated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    authority_grant_id VARCHAR(32) NOT NULL CHECK (authority_grant_id ~ '^[0-9a-f]{32}$'),
    authority_category VARCHAR(32) NOT NULL CHECK (authority_category = 'write'),
    authority_binding_digest VARCHAR(64) NOT NULL CHECK (authority_binding_digest ~ '^[0-9a-f]{64}$'),
    authority_expires_at TIMESTAMPTZ NOT NULL,
    authority_consumed_at TIMESTAMPTZ NOT NULL,
    state VARCHAR(32) NOT NULL CHECK (state IN ('queued', 'running', 'verifying', 'succeeded', 'degraded', 'needs_attention', 'failed', 'cancel_requested', 'canceled')),
    failure_category VARCHAR(64) NOT NULL DEFAULT '',
    transition_revision BIGINT NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    workspace_phase VARCHAR(32) NOT NULL CHECK (workspace_phase IN ('none', 'reserved', 'marker_created', 'writing', 'sealed', 'published', 'cleanup_due', 'workspace_cleaned')),
    encrypted_workspace_relative_locator TEXT NOT NULL DEFAULT '',
    workspace_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (workspace_binding_digest = ''
        OR workspace_binding_digest ~ '^[0-9a-f]{64}$'),
    workspace_marker_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (workspace_marker_binding_digest = ''
        OR workspace_marker_binding_digest ~ '^[0-9a-f]{64}$'),
    workspace_owner VARCHAR(64) NOT NULL DEFAULT '',
    workspace_fence BIGINT NOT NULL DEFAULT 0 CHECK (workspace_fence >= 0),
    workspace_marker_validation_attempt_id VARCHAR(32) NOT NULL DEFAULT '' CHECK (
        workspace_marker_validation_attempt_id = ''
        OR workspace_marker_validation_attempt_id ~ '^[0-9a-f]{32}$'
    ),
    workspace_marker_validation_attempt_fence BIGINT NOT NULL DEFAULT 0
        CHECK (workspace_marker_validation_attempt_fence >= 0),
    workspace_marker_validation_node_fence BIGINT NOT NULL DEFAULT 0
        CHECK (workspace_marker_validation_node_fence >= 0),
    workspace_cleanup_phase VARCHAR(32) NOT NULL DEFAULT 'claimed'
        CHECK (workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted', 'tombstoned')),
    workspace_cleanup_owner VARCHAR(64) NOT NULL DEFAULT '',
    workspace_cleanup_lease_expires_at TIMESTAMPTZ,
    workspace_cleanup_fence BIGINT NOT NULL DEFAULT 0 CHECK (workspace_cleanup_fence >= 0),
    workspace_cleanup_node_lease_id VARCHAR(32)
        CHECK (workspace_cleanup_node_lease_id IS NULL OR workspace_cleanup_node_lease_id ~ '^[0-9a-f]{32}$'),
    workspace_cleanup_node_fence BIGINT NOT NULL DEFAULT 0 CHECK (workspace_cleanup_node_fence >= 0),
    workspace_cleanup_attempt BIGINT NOT NULL DEFAULT 0 CHECK (workspace_cleanup_attempt >= 0),
    plaintext_deadline TIMESTAMPTZ,
    target_mode VARCHAR(16) NOT NULL CHECK (target_mode IN ('isolated', 'in_place')),
    target_node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    target_root_id VARCHAR(32) NOT NULL CHECK (length(target_root_id) BETWEEN 1 AND 32),
    root_locator_digest VARCHAR(64) NOT NULL CHECK (root_locator_digest ~ '^[0-9a-f]{64}$'),
    path_digest VARCHAR(64) NOT NULL CHECK (path_digest ~ '^[0-9a-f]{64}$'),
    target_chain_revision VARCHAR(64) NOT NULL DEFAULT '' CHECK (length(target_chain_revision) <= 64),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, plan_id),
    UNIQUE (id, target_mode, target_node_id),
    CHECK (
        (target_mode = 'in_place' AND workspace_phase = 'none'
            AND encrypted_workspace_relative_locator = '' AND workspace_binding_digest = ''
            AND workspace_marker_binding_digest = ''
            AND workspace_owner = '' AND workspace_fence = 0
            AND workspace_marker_validation_attempt_id = ''
            AND workspace_marker_validation_attempt_fence = 0
            AND workspace_marker_validation_node_fence = 0
            AND plaintext_deadline IS NULL)
        OR (target_mode = 'isolated' AND (
            (workspace_phase = 'none' AND encrypted_workspace_relative_locator <> ''
                AND length(workspace_binding_digest) = 64
                AND workspace_marker_binding_digest = '' AND workspace_owner = ''
                AND workspace_fence = 0
                AND workspace_marker_validation_attempt_id = ''
                AND workspace_marker_validation_attempt_fence = 0
                AND workspace_marker_validation_node_fence = 0
                AND plaintext_deadline IS NULL)
            OR (workspace_phase = 'reserved'
                AND encrypted_workspace_relative_locator <> '' AND length(workspace_binding_digest) = 64
                AND length(workspace_marker_binding_digest) = 64
                AND workspace_owner <> '' AND workspace_fence > 0
                AND workspace_marker_validation_attempt_id = ''
                AND workspace_marker_validation_attempt_fence = 0
                AND workspace_marker_validation_node_fence = 0
                AND plaintext_deadline IS NOT NULL)
            OR (workspace_phase IN ('marker_created', 'writing', 'sealed', 'published')
                AND encrypted_workspace_relative_locator <> '' AND length(workspace_binding_digest) = 64
                AND length(workspace_marker_binding_digest) = 64
                AND workspace_owner <> '' AND workspace_fence > 0
                AND workspace_marker_validation_attempt_id ~ '^[0-9a-f]{32}$'
                AND workspace_marker_validation_attempt_fence > 0
                AND workspace_marker_validation_node_fence > 0
                AND plaintext_deadline IS NOT NULL)
            OR (workspace_phase IN ('cleanup_due', 'workspace_cleaned')
                AND encrypted_workspace_relative_locator <> '' AND length(workspace_binding_digest) = 64
                AND length(workspace_marker_binding_digest) = 64
                AND workspace_owner <> '' AND workspace_fence > 0 AND plaintext_deadline IS NOT NULL
                AND (
                    (workspace_marker_validation_attempt_id = ''
                        AND workspace_marker_validation_attempt_fence = 0
                        AND workspace_marker_validation_node_fence = 0)
                    OR (workspace_marker_validation_attempt_id ~ '^[0-9a-f]{32}$'
                        AND workspace_marker_validation_attempt_fence > 0
                        AND workspace_marker_validation_node_fence > 0)
                ))
        ))
    ),
    CHECK (workspace_phase <> 'published' OR state IN ('succeeded', 'degraded')),
    CHECK (
        state NOT IN ('failed', 'canceled', 'needs_attention')
        OR workspace_phase IN ('none', 'cleanup_due', 'workspace_cleaned')
    ),
    CHECK (
        state NOT IN ('succeeded', 'degraded')
        OR workspace_phase IN ('none', 'sealed', 'published', 'cleanup_due', 'workspace_cleaned')
    ),
    CHECK (
        (workspace_phase NOT IN ('cleanup_due', 'workspace_cleaned')
            AND workspace_cleanup_phase = 'claimed'
            AND workspace_cleanup_owner = '' AND workspace_cleanup_lease_expires_at IS NULL
            AND workspace_cleanup_fence = 0 AND workspace_cleanup_node_lease_id IS NULL
            AND workspace_cleanup_node_fence = 0 AND workspace_cleanup_attempt = 0)
        OR (workspace_phase = 'cleanup_due' AND (
            (workspace_cleanup_phase = 'claimed'
                AND workspace_cleanup_owner = '' AND workspace_cleanup_lease_expires_at IS NULL
                AND workspace_cleanup_fence = 0 AND workspace_cleanup_node_lease_id IS NULL
                AND workspace_cleanup_node_fence = 0 AND workspace_cleanup_attempt = 0)
            OR (workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted')
                AND workspace_cleanup_owner <> '' AND workspace_cleanup_lease_expires_at IS NOT NULL
                AND workspace_cleanup_fence > 0 AND workspace_cleanup_node_lease_id IS NOT NULL
                AND workspace_cleanup_node_fence > 0 AND workspace_cleanup_attempt > 0)
            OR (workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted')
                AND workspace_cleanup_owner = '' AND workspace_cleanup_lease_expires_at IS NULL
                AND workspace_cleanup_fence > 0 AND workspace_cleanup_node_lease_id IS NULL
                AND workspace_cleanup_node_fence = 0 AND workspace_cleanup_attempt > 0)
        ))
        OR (workspace_phase = 'workspace_cleaned'
            AND workspace_cleanup_phase = 'tombstoned'
            AND workspace_cleanup_owner = '' AND workspace_cleanup_lease_expires_at IS NULL
            AND workspace_cleanup_fence > 0 AND workspace_cleanup_node_lease_id IS NULL
            AND workspace_cleanup_node_fence = 0 AND workspace_cleanup_attempt > 0)
    ),
    CHECK (workspace_cleanup_lease_expires_at IS NULL OR workspace_cleanup_lease_expires_at > created_at),
    CHECK (plaintext_deadline IS NULL OR plaintext_deadline > created_at),
    CHECK (created_at < preflight_expires_at AND created_at < authority_expires_at
        AND authority_consumed_at <= created_at AND authority_consumed_at < authority_expires_at),
    CHECK ((security_decision = 'admin_override' AND length(security_override_binding_digest) = 64)
        OR (security_decision = 'allow_clean' AND security_override_binding_digest = '')),
    CHECK (failure_category <> 'remote_outcome_unresolved' OR state = 'needs_attention'),
    CHECK (created_at <= updated_at),
    FOREIGN KEY (
        preflight_id, plan_id, target_node_id, target_root_id,
        root_locator_digest, path_digest, preflight_node_revision
    ) REFERENCES backup_asset_recovery_preflights(
        id, plan_id, target_node_id, target_root_id,
        root_locator_digest, path_digest, node_revision
    ) ON DELETE RESTRICT,
    FOREIGN KEY (plan_id, target_mode, target_node_id)
        REFERENCES backup_asset_recovery_plans(id, target_mode, target_node_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_jobs_plan ON backup_asset_recovery_jobs(plan_id);
CREATE UNIQUE INDEX idx_backup_asset_recovery_jobs_id_target_node
    ON backup_asset_recovery_jobs(id, target_node_id);
CREATE INDEX idx_backup_asset_recovery_jobs_claim ON backup_asset_recovery_jobs(state, updated_at, id);

CREATE TABLE backup_asset_recovery_job_items (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plan_id VARCHAR(32) NOT NULL CHECK (plan_id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    plan_item_id VARCHAR(32) CHECK (plan_item_id IS NULL OR plan_item_id ~ '^[0-9a-f]{32}$'),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    operation_kind VARCHAR(16) NOT NULL CHECK (operation_kind IN ('create', 'overwrite', 'skip', 'delete')),
    target_path_digest VARCHAR(64) NOT NULL CHECK (target_path_digest ~ '^[0-9a-f]{64}$'),
    semantic_target_digest VARCHAR(64) NOT NULL CHECK (semantic_target_digest ~ '^[0-9a-f]{64}$'),
    target_object_digest VARCHAR(64) NOT NULL CHECK (target_object_digest ~ '^[0-9a-f]{64}$'),
    expected_prior_kind VARCHAR(16) NOT NULL CHECK (expected_prior_kind IN ('absent', 'present')),
    expected_prior_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (expected_prior_digest = ''
        OR expected_prior_digest ~ '^[0-9a-f]{64}$'),
    expected_post_identity_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (expected_post_identity_digest = ''
        OR expected_post_identity_digest ~ '^[0-9a-f]{64}$'),
    expected_post_bytes BIGINT NOT NULL DEFAULT -1 CHECK (expected_post_bytes >= -1),
    expected_prior_bytes BIGINT NOT NULL DEFAULT -1 CHECK (expected_prior_bytes >= -1),
    encrypted_target_relative_locator TEXT NOT NULL CHECK (encrypted_target_relative_locator <> ''),
    target_locator_key_version INTEGER NOT NULL CHECK (target_locator_key_version > 0),
    target_locator_cipher_version INTEGER NOT NULL CHECK (target_locator_cipher_version > 0),
    display_class VARCHAR(16) NOT NULL CHECK (display_class IN ('regular', 'directory', 'link', 'special')),
    estimated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    outcome VARCHAR(32) NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'failed', 'skipped')),
    bytes_written BIGINT NOT NULL DEFAULT 0 CHECK (bytes_written >= 0),
    verified_size BIGINT NOT NULL DEFAULT 0 CHECK (verified_size >= 0),
    verified_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (verified_digest = ''
        OR verified_digest ~ '^[0-9a-f]{64}$'),
    failure_category VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, job_id),
    UNIQUE (job_id, ordinal),
    UNIQUE (job_id, plan_item_id),
    UNIQUE (job_id, target_path_digest),
    UNIQUE (job_id, semantic_target_digest),
    UNIQUE (job_id, target_object_digest),
    CHECK (semantic_target_digest <> target_object_digest),
    CHECK (
        (operation_kind = 'delete' AND plan_item_id IS NULL)
        OR (operation_kind IN ('create', 'overwrite', 'skip') AND plan_item_id IS NOT NULL)
    ),
    CHECK (
        (operation_kind = 'create'
            AND expected_prior_kind = 'absent' AND expected_prior_digest = ''
            AND length(expected_post_identity_digest) = 64
            AND expected_post_bytes >= 0 AND expected_prior_bytes = -1)
        OR (operation_kind = 'overwrite'
            AND expected_prior_kind = 'present' AND length(expected_prior_digest) = 64
            AND length(expected_post_identity_digest) = 64
            AND expected_post_bytes >= 0 AND expected_prior_bytes >= 0)
        OR (operation_kind = 'skip'
            AND expected_prior_kind = 'present' AND length(expected_prior_digest) = 64
            AND expected_post_identity_digest = expected_prior_digest
            AND expected_post_bytes = -1 AND expected_prior_bytes >= 0)
        OR (operation_kind = 'delete'
            AND expected_prior_kind = 'present' AND length(expected_prior_digest) = 64
            AND expected_post_identity_digest = ''
            AND expected_post_bytes = -1 AND expected_prior_bytes = -1)
    ),
    CHECK (
        (outcome = '' AND bytes_written = 0 AND verified_size = 0 AND verified_digest = ''
            AND failure_category = '')
        OR (outcome = 'succeeded' AND failure_category = '' AND (
            (operation_kind IN ('create', 'overwrite')
                AND bytes_written = expected_post_bytes
                AND verified_size = expected_post_bytes
                AND verified_digest = expected_post_identity_digest)
            OR (operation_kind = 'delete' AND bytes_written = 0
                AND verified_size = 0 AND verified_digest = '')
        ))
        OR (outcome = 'skipped' AND bytes_written = 0
            AND verified_size = expected_prior_bytes
            AND verified_digest = expected_prior_digest
            AND failure_category = '')
        OR (outcome = 'failed' AND failure_category <> '')
    ),
    CHECK (
        (operation_kind = 'skip' AND outcome IN ('', 'skipped', 'failed'))
        OR (operation_kind <> 'skip' AND outcome IN ('', 'succeeded', 'failed'))
    ),
    CHECK (failure_category <> 'remote_outcome_unresolved' OR outcome = 'failed'),
    FOREIGN KEY (job_id, plan_id) REFERENCES backup_asset_recovery_jobs(id, plan_id) ON DELETE CASCADE,
    FOREIGN KEY (plan_item_id, plan_id)
        REFERENCES backup_asset_recovery_plan_items(id, plan_id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_recovery_job_items_job_outcome
    ON backup_asset_recovery_job_items(job_id, outcome, ordinal);

CREATE TABLE backup_asset_recovery_attempts (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_jobs(id) ON DELETE CASCADE,
    owner_id VARCHAR(64) NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 64),
    fence BIGINT NOT NULL CHECK (fence > 0),
    state VARCHAR(32) NOT NULL CHECK (state IN ('claimed', 'running', 'completed', 'failed', 'canceled', 'superseded', 'lost')),
    mutation_armed BOOLEAN NOT NULL DEFAULT FALSE,
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, job_id),
    UNIQUE (job_id, fence),
    CHECK ((state IN ('claimed', 'running') AND lease_expires_at IS NOT NULL AND heartbeat_at IS NOT NULL AND closed_at IS NULL)
        OR (state IN ('completed', 'failed', 'canceled', 'superseded', 'lost') AND closed_at IS NOT NULL)),
    CHECK (NOT mutation_armed OR state IN ('running', 'completed', 'failed', 'canceled', 'lost')),
    CHECK (heartbeat_at IS NULL OR heartbeat_at >= created_at),
    CHECK (closed_at IS NULL OR closed_at >= created_at)
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_attempts_current
    ON backup_asset_recovery_attempts(job_id) WHERE state IN ('claimed', 'running');
CREATE INDEX idx_backup_asset_recovery_attempts_expiry
    ON backup_asset_recovery_attempts(state, lease_expires_at, updated_at);
CREATE OR REPLACE FUNCTION backup_asset_recovery_attempt_mutation_arm_monotonic()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (OLD.mutation_armed AND NOT NEW.mutation_armed)
       OR (OLD.state IN ('completed', 'failed', 'canceled', 'superseded', 'lost')
           AND NEW.mutation_armed IS DISTINCT FROM OLD.mutation_armed) THEN
        RAISE EXCEPTION 'mutation_armed is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_attempts_mutation_arm_monotonic
BEFORE UPDATE OF mutation_armed ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_mutation_arm_monotonic();

CREATE OR REPLACE FUNCTION backup_asset_recovery_attempt_integrity_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.job_id IS DISTINCT FROM OLD.job_id
        OR (NEW.owner_id IS DISTINCT FROM OLD.owner_id AND NOT (
            OLD.owner_id = 'recovery-authorization'
            AND OLD.state = 'claimed' AND NEW.state = 'running'
            AND NOT OLD.mutation_armed AND NOT NEW.mutation_armed
            AND EXISTS (
                SELECT 1 FROM backup_asset_recovery_jobs
                WHERE id = NEW.job_id AND state = 'queued'
            )
        ))
        OR NEW.fence IS DISTINCT FROM OLD.fence
        OR (OLD.state IN ('completed', 'failed', 'canceled', 'superseded', 'lost')
            AND NEW.state IS DISTINCT FROM OLD.state)
        OR (NEW.state IN ('claimed', 'running') AND EXISTS (
            SELECT 1 FROM backup_asset_recovery_jobs
            WHERE id = NEW.job_id AND state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'canceled')
        )) THEN
        RAISE EXCEPTION 'recovery attempt identity, terminal state, and terminal job barrier are immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_attempts_integrity
BEFORE UPDATE ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_integrity_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_attempt_terminal_delete_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF EXISTS (
            SELECT 1 FROM backup_asset_recovery_attempts
            WHERE id = NEW.id
              AND (mutation_armed OR state IN ('completed', 'failed', 'canceled', 'superseded', 'lost'))
        ) THEN
            RAISE EXCEPTION 'terminal or mutation-armed recovery attempt cannot be replaced';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.mutation_armed
       OR OLD.state IN ('completed', 'failed', 'canceled', 'superseded', 'lost') THEN
        RAISE EXCEPTION 'terminal or mutation-armed recovery attempt cannot be deleted';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_attempts_terminal_delete
BEFORE DELETE ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_terminal_delete_guard();

CREATE TRIGGER trg_backup_asset_recovery_attempts_terminal_replay
BEFORE INSERT ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_terminal_delete_guard();

CREATE TABLE backup_asset_recovery_checkpoints (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    job_item_id VARCHAR(32) NOT NULL DEFAULT '' CHECK (job_item_id = '' OR job_item_id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) NOT NULL CHECK (attempt_id ~ '^[0-9a-f]{32}$'),
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    phase VARCHAR(32) NOT NULL CHECK (phase IN ('operation', 'operation_unresolved', 'delete_authority_required', 'delete_authority_consumed', 'verification', 'workspace_reserved')),
    authority_category VARCHAR(32) NOT NULL DEFAULT '' CHECK (authority_category IN ('', 'write', 'exact_mirror_delete')),
    operation_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (operation_digest = ''
        OR operation_digest ~ '^[0-9a-f]{64}$'),
    prior_target_revision VARCHAR(64) NOT NULL DEFAULT '' CHECK (length(prior_target_revision) <= 64),
    next_target_revision VARCHAR(64) NOT NULL DEFAULT '' CHECK (length(next_target_revision) <= 64),
    unresolved_category VARCHAR(32) NOT NULL DEFAULT '' CHECK (unresolved_category IN ('',
        'revision_disagreement', 'verification_mismatch', 'write_result_invalid', 'observation_invalid')),
    write_result_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (write_result_digest = '' OR write_result_digest ~ '^[0-9a-f]{64}$'),
    write_target_revision VARCHAR(64) NOT NULL DEFAULT '',
    observation_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (observation_digest = '' OR observation_digest ~ '^[0-9a-f]{64}$'),
    observed_target_revision VARCHAR(64) NOT NULL DEFAULT '',
    observed_presence VARCHAR(16) NOT NULL DEFAULT '' CHECK (observed_presence IN ('', 'present', 'absent')),
    source_revalidation_outcome VARCHAR(16) NOT NULL DEFAULT '' CHECK (source_revalidation_outcome IN ('', 'matched', 'drifted', 'failed')),
    node_fence BIGINT NOT NULL DEFAULT 0 CHECK (node_fence >= 0),
    attempt_fence BIGINT NOT NULL DEFAULT 0 CHECK (attempt_fence >= 0),
    plan_binding_digest VARCHAR(64) NOT NULL CHECK (plan_binding_digest ~ '^[0-9a-f]{64}$'),
    source_revision_digest VARCHAR(64) NOT NULL CHECK (source_revision_digest ~ '^[0-9a-f]{64}$'),
    preflight_id VARCHAR(32) NOT NULL CHECK (preflight_id ~ '^[0-9a-f]{32}$'),
    preflight_revision VARCHAR(64) NOT NULL CHECK (length(preflight_revision) BETWEEN 1 AND 64),
    preflight_expires_at TIMESTAMPTZ NOT NULL,
    security_decision VARCHAR(32) NOT NULL CHECK (security_decision IN ('allow_clean', 'admin_override')),
    security_decision_digest VARCHAR(64) NOT NULL CHECK (security_decision_digest ~ '^[0-9a-f]{64}$'),
    security_finding_set_digest VARCHAR(64) NOT NULL CHECK (security_finding_set_digest ~ '^[0-9a-f]{64}$'),
    security_policy_revision VARCHAR(64) NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 64),
    authority_grant_id VARCHAR(32) NOT NULL CHECK (authority_grant_id ~ '^[0-9a-f]{32}$'),
    job_authority_category VARCHAR(32) NOT NULL CHECK (job_authority_category = 'write'),
    authority_binding_digest VARCHAR(64) NOT NULL CHECK (authority_binding_digest ~ '^[0-9a-f]{64}$'),
    authority_expires_at TIMESTAMPTZ NOT NULL,
    delete_node_revision VARCHAR(64) NOT NULL DEFAULT '',
    delete_root_revision VARCHAR(64) NOT NULL DEFAULT '',
    delete_authority_expires_at TIMESTAMPTZ,
    delete_grant_id VARCHAR(32) NOT NULL DEFAULT '' CHECK (delete_grant_id = '' OR delete_grant_id ~ '^[0-9a-f]{32}$'),
    delete_grant_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (delete_grant_binding_digest = ''
        OR delete_grant_binding_digest ~ '^[0-9a-f]{64}$'),
    delete_grant_expires_at TIMESTAMPTZ,
    delete_grant_consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (job_id, sequence),
    CHECK (
        (phase = 'operation' AND authority_category = 'write' AND length(operation_digest) = 64
            AND prior_target_revision <> '' AND next_target_revision <> '' AND node_fence > 0 AND attempt_fence > 0
            AND delete_node_revision = '' AND delete_root_revision = ''
            AND delete_authority_expires_at IS NULL AND delete_grant_id = ''
            AND delete_grant_binding_digest = '' AND delete_grant_expires_at IS NULL
            AND delete_grant_consumed_at IS NULL)
        OR (phase = 'delete_authority_required' AND authority_category = 'exact_mirror_delete'
            AND length(operation_digest) = 64 AND prior_target_revision <> '' AND next_target_revision = ''
            AND node_fence > 0 AND attempt_fence > 0 AND delete_node_revision <> ''
            AND delete_root_revision <> '' AND delete_authority_expires_at > created_at
            AND delete_grant_id = '' AND delete_grant_binding_digest = ''
            AND delete_grant_expires_at IS NULL AND delete_grant_consumed_at IS NULL)
        OR (phase = 'delete_authority_consumed' AND authority_category = 'exact_mirror_delete'
            AND length(operation_digest) = 64 AND prior_target_revision <> ''
            AND next_target_revision = prior_target_revision AND node_fence > 0 AND attempt_fence > 0
            AND delete_node_revision <> '' AND delete_root_revision <> ''
            AND delete_authority_expires_at > created_at AND delete_grant_id <> ''
            AND delete_grant_binding_digest <> '' AND delete_grant_expires_at > created_at
            AND delete_grant_consumed_at = created_at)
        OR (phase IN ('verification', 'workspace_reserved') AND authority_category = '' AND operation_digest = ''
            AND prior_target_revision = '' AND next_target_revision = '' AND node_fence = 0 AND attempt_fence = 0
            AND delete_node_revision = '' AND delete_root_revision = ''
            AND delete_authority_expires_at IS NULL AND delete_grant_id = ''
            AND delete_grant_binding_digest = '' AND delete_grant_expires_at IS NULL
            AND delete_grant_consumed_at IS NULL)
        OR (phase = 'operation_unresolved' AND authority_category = 'write'
            AND length(operation_digest) = 64 AND prior_target_revision <> ''
            AND next_target_revision = '' AND node_fence > 0 AND attempt_fence > 0
            AND delete_node_revision = '' AND delete_root_revision = ''
            AND delete_authority_expires_at IS NULL AND delete_grant_id = ''
            AND delete_grant_binding_digest = '' AND delete_grant_expires_at IS NULL
            AND delete_grant_consumed_at IS NULL)
    ),
    CHECK (
        (phase = 'operation' AND length(job_item_id) = 32
            AND unresolved_category = '' AND write_result_digest = '' AND write_target_revision = ''
            AND observation_digest = '' AND observed_target_revision = ''
            AND observed_presence = '' AND source_revalidation_outcome = '')
        OR
        (phase = 'operation_unresolved' AND length(job_item_id) = 32
            AND unresolved_category <> '' AND source_revalidation_outcome <> '')
        OR (phase NOT IN ('operation', 'operation_unresolved')
            AND job_item_id = '' AND unresolved_category = ''
            AND write_result_digest = '' AND write_target_revision = ''
            AND observation_digest = '' AND observed_target_revision = ''
            AND observed_presence = '' AND source_revalidation_outcome = '')
    ),
    FOREIGN KEY (attempt_id, job_id) REFERENCES backup_asset_recovery_attempts(id, job_id) ON DELETE CASCADE
);
CREATE INDEX idx_backup_asset_recovery_checkpoints_job_sequence
    ON backup_asset_recovery_checkpoints(job_id, sequence);
CREATE UNIQUE INDEX idx_backup_asset_recovery_checkpoints_delete_grant
    ON backup_asset_recovery_checkpoints(delete_grant_id) WHERE delete_grant_id <> '';

CREATE TABLE backup_asset_recovery_evidence (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) CHECK (job_id IS NULL OR job_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_jobs(id) ON DELETE CASCADE,
    kind VARCHAR(32) NOT NULL CHECK (kind IN ('verification', 'difference', 'failure', 'authorization_receipt', 'schema_use_latch', 'scheduler_state')),
    outcome VARCHAR(32) NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'degraded', 'failed', 'needs_attention')),
    summary_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (summary_digest = ''
        OR summary_digest ~ '^[0-9a-f]{64}$'),
    difference_count BIGINT NOT NULL DEFAULT 0 CHECK (difference_count >= 0),
    verified_at TIMESTAMPTZ,
    plan_id VARCHAR(32) CHECK (plan_id IS NULL OR plan_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_plans(id) ON DELETE RESTRICT,
    checkpoint_id VARCHAR(32) CHECK (checkpoint_id IS NULL OR checkpoint_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_checkpoints(id) ON DELETE RESTRICT,
    grant_id VARCHAR(32) CHECK (grant_id IS NULL OR grant_id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) CHECK (attempt_id IS NULL OR attempt_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_attempts(id) ON DELETE RESTRICT,
    source_lease_id TEXT REFERENCES recovery_point_leases(id) ON DELETE RESTRICT,
    node_lease_id VARCHAR(32) CHECK (node_lease_id IS NULL OR node_lease_id ~ '^[0-9a-f]{32}$'),
    requester_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    operation VARCHAR(48) NOT NULL DEFAULT '' CHECK (operation IN ('', 'security_override', 'write_authorize', 'exact_mirror_delete_authorize', 'execute')),
    category VARCHAR(32) NOT NULL DEFAULT '' CHECK (category IN ('', 'security_override', 'write', 'exact_mirror_delete', 'execute')),
    endpoint VARCHAR(96) NOT NULL DEFAULT '' CHECK (length(endpoint) <= 96),
    idempotency_key_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (idempotency_key_digest = ''
        OR idempotency_key_digest ~ '^[0-9a-f]{64}$'),
    intent_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (intent_digest = '' OR intent_digest ~ '^[0-9a-f]{64}$'),
    step_up_jti_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (step_up_jti_digest = '' OR step_up_jti_digest ~ '^[0-9a-f]{64}$'),
    presenting_session_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (presenting_session_digest = ''
        OR presenting_session_digest ~ '^[0-9a-f]{64}$'),
    presenting_session_user_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    presenting_session_role VARCHAR(32) NOT NULL DEFAULT '' CHECK (presenting_session_role IN ('', 'admin')),
    presenting_session_token_version BIGINT NOT NULL DEFAULT 0 CHECK (presenting_session_token_version >= 0),
    proof_expires_at TIMESTAMPTZ,
    presenting_session_expires_at TIMESTAMPTZ,
    replay_expires_at TIMESTAMPTZ,
    expected_plan_transition_revision BIGINT NOT NULL DEFAULT 0 CHECK (expected_plan_transition_revision >= 0),
    result_plan_transition_revision BIGINT NOT NULL DEFAULT 0 CHECK (result_plan_transition_revision >= 0),
    grant_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (grant_binding_digest = '' OR grant_binding_digest ~ '^[0-9a-f]{64}$'),
    source_lease_binding_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (source_lease_binding_digest = ''
        OR source_lease_binding_digest ~ '^[0-9a-f]{64}$'),
    node_lease_fence BIGINT NOT NULL DEFAULT 0 CHECK (node_lease_fence >= 0),
    scheduler_scope VARCHAR(16) NOT NULL DEFAULT '' CHECK (scheduler_scope IN ('', 'claim', 'takeover')),
    scheduler_cursor_at TIMESTAMPTZ,
    scheduler_cursor_id VARCHAR(32) NOT NULL DEFAULT '' CHECK (scheduler_cursor_id = ''
        OR scheduler_cursor_id ~ '^[0-9a-f]{32}$'),
    scheduler_high_water_at TIMESTAMPTZ,
    scheduler_high_water_id VARCHAR(32) NOT NULL DEFAULT '' CHECK (scheduler_high_water_id = ''
        OR scheduler_high_water_id ~ '^[0-9a-f]{32}$'),
    scheduler_revision BIGINT NOT NULL DEFAULT 0 CHECK (scheduler_revision >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK ((scheduler_cursor_at IS NULL AND scheduler_cursor_id = '')
        OR (scheduler_cursor_at IS NOT NULL AND scheduler_cursor_id <> '')),
    CHECK ((scheduler_high_water_at IS NULL AND scheduler_high_water_id = '')
        OR (scheduler_high_water_at IS NOT NULL AND scheduler_high_water_id <> '')),
    CHECK (scheduler_cursor_at IS NULL OR (scheduler_high_water_at IS NOT NULL AND
        (scheduler_cursor_at < scheduler_high_water_at
            OR (scheduler_cursor_at = scheduler_high_water_at AND scheduler_cursor_id <= scheduler_high_water_id)))),
    CHECK (
        (id = '00000000000000000000000000000069' AND kind = 'schema_use_latch'
            AND job_id IS NULL AND outcome = '' AND summary_digest = '' AND difference_count = 0 AND verified_at IS NULL
            AND plan_id IS NULL AND checkpoint_id IS NULL AND grant_id IS NULL
            AND attempt_id IS NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
            AND requester_id IS NULL AND operation = '' AND category = '' AND endpoint = ''
            AND idempotency_key_digest = '' AND intent_digest = '' AND step_up_jti_digest = ''
            AND presenting_session_digest = '' AND presenting_session_user_id IS NULL
            AND presenting_session_role = '' AND presenting_session_token_version = 0
            AND proof_expires_at IS NULL AND presenting_session_expires_at IS NULL
            AND replay_expires_at IS NULL AND expected_plan_transition_revision = 0
            AND result_plan_transition_revision = 0 AND grant_binding_digest = ''
            AND source_lease_binding_digest = '' AND node_lease_fence = 0
            AND scheduler_scope = '' AND scheduler_cursor_at IS NULL AND scheduler_cursor_id = ''
            AND scheduler_high_water_at IS NULL AND scheduler_high_water_id = '' AND scheduler_revision = 0)
        OR (id <> '00000000000000000000000000000069' AND kind IN ('verification', 'difference', 'failure')
            AND job_id IS NOT NULL AND outcome <> ''
            AND plan_id IS NOT NULL AND checkpoint_id IS NOT NULL AND grant_id IS NOT NULL
            AND attempt_id IS NOT NULL AND source_lease_id IS NOT NULL AND node_lease_id IS NOT NULL
            AND requester_id IS NULL AND operation = '' AND category = '' AND endpoint = ''
            AND idempotency_key_digest = '' AND intent_digest = '' AND step_up_jti_digest = ''
            AND presenting_session_digest = '' AND presenting_session_user_id IS NULL
            AND presenting_session_role = '' AND presenting_session_token_version = 0
            AND proof_expires_at IS NULL AND presenting_session_expires_at IS NULL
            AND replay_expires_at IS NULL AND expected_plan_transition_revision = 0
            AND result_plan_transition_revision = 0 AND grant_binding_digest = ''
            AND source_lease_binding_digest = '' AND node_lease_fence > 0
            AND scheduler_scope = '' AND scheduler_cursor_at IS NULL AND scheduler_cursor_id = ''
            AND scheduler_high_water_at IS NULL AND scheduler_high_water_id = '' AND scheduler_revision = 0)
        OR (id <> '00000000000000000000000000000069' AND kind = 'authorization_receipt'
            AND outcome = '' AND summary_digest = '' AND difference_count = 0 AND verified_at IS NULL
            AND plan_id IS NOT NULL AND requester_id IS NOT NULL AND requester_id > 0
            AND length(endpoint) > 0 AND length(idempotency_key_digest) = 64
            AND length(intent_digest) = 64 AND length(step_up_jti_digest) = 64
            AND length(presenting_session_digest) = 64 AND presenting_session_user_id IS NOT NULL
            AND presenting_session_user_id = requester_id AND presenting_session_role = 'admin'
            AND presenting_session_token_version > 0 AND proof_expires_at IS NOT NULL
            AND replay_expires_at IS NOT NULL AND presenting_session_expires_at IS NOT NULL
            AND created_at < proof_expires_at
            AND proof_expires_at <= replay_expires_at
            AND replay_expires_at <= presenting_session_expires_at
            AND expected_plan_transition_revision > 0
            AND (
                (operation = 'security_override' AND category = 'security_override'
                    AND endpoint = '/api/v1/recovery-plans/:id/security-overrides'
                    AND job_id IS NULL AND checkpoint_id IS NULL AND grant_id IS NULL
                    AND attempt_id IS NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision + 1
                    AND grant_binding_digest = '' AND source_lease_binding_digest = '' AND node_lease_fence = 0)
                OR (operation = 'write_authorize' AND category = 'write'
                    AND endpoint = '/api/v1/recovery-plans/:id/write-authorizations'
                    AND job_id IS NULL AND checkpoint_id IS NULL AND grant_id IS NOT NULL
                    AND attempt_id IS NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision + 1
                    AND length(grant_binding_digest) = 64 AND source_lease_binding_digest = '' AND node_lease_fence = 0)
                OR (operation = 'exact_mirror_delete_authorize' AND category = 'exact_mirror_delete'
                    AND endpoint = '/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations'
                    AND job_id IS NOT NULL AND checkpoint_id IS NOT NULL AND grant_id IS NOT NULL
                    AND attempt_id IS NOT NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision
                    AND length(grant_binding_digest) = 64 AND source_lease_binding_digest = '' AND node_lease_fence = 0)
                OR (operation = 'execute' AND category = 'execute'
                    AND endpoint = '/api/v1/recovery-plans/:id/execute'
                    AND job_id IS NOT NULL AND checkpoint_id IS NULL AND grant_id IS NOT NULL
                    AND attempt_id IS NOT NULL AND source_lease_id IS NOT NULL AND node_lease_id IS NOT NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision + 1
                    AND length(grant_binding_digest) = 64 AND length(source_lease_binding_digest) = 64
                    AND node_lease_fence > 0)
            )
            AND scheduler_scope = '' AND scheduler_cursor_at IS NULL AND scheduler_cursor_id = ''
            AND scheduler_high_water_at IS NULL AND scheduler_high_water_id = '' AND scheduler_revision = 0)
        OR (kind = 'scheduler_state'
            AND ((id = '0000000000000000000000000000006a' AND scheduler_scope = 'claim')
                OR (id = '0000000000000000000000000000006b' AND scheduler_scope = 'takeover'))
            AND job_id IS NULL AND outcome = '' AND summary_digest = '' AND difference_count = 0 AND verified_at IS NULL
            AND plan_id IS NULL AND checkpoint_id IS NULL AND grant_id IS NULL
            AND attempt_id IS NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
            AND requester_id IS NULL AND operation = '' AND category = '' AND endpoint = ''
            AND idempotency_key_digest = '' AND intent_digest = '' AND step_up_jti_digest = ''
            AND presenting_session_digest = '' AND presenting_session_user_id IS NULL
            AND presenting_session_role = '' AND presenting_session_token_version = 0
            AND proof_expires_at IS NULL AND presenting_session_expires_at IS NULL AND replay_expires_at IS NULL
            AND expected_plan_transition_revision = 0 AND result_plan_transition_revision = 0
            AND grant_binding_digest = '' AND source_lease_binding_digest = '' AND node_lease_fence = 0
            AND scheduler_revision > 0)
    ),
    CHECK (verified_at IS NULL OR verified_at >= created_at),
    CHECK (created_at <= updated_at)
);
CREATE INDEX idx_backup_asset_recovery_evidence_job_created
    ON backup_asset_recovery_evidence(job_id, created_at);
CREATE UNIQUE INDEX idx_backup_asset_recovery_evidence_authorization_idempotency
    ON backup_asset_recovery_evidence(requester_id, endpoint, idempotency_key_digest)
    WHERE kind = 'authorization_receipt';
CREATE UNIQUE INDEX idx_backup_asset_recovery_evidence_authorization_proof
    ON backup_asset_recovery_evidence(step_up_jti_digest)
    WHERE kind = 'authorization_receipt';
CREATE INDEX idx_backup_asset_recovery_evidence_authorization_reaper
    ON backup_asset_recovery_evidence(kind, replay_expires_at, id)
    WHERE kind = 'authorization_receipt';
CREATE INDEX idx_recovery_point_leases_recovery_job_owner
    ON recovery_point_leases(holder_type, owner_id, attempt_id, recovery_point_id, id)
    WHERE holder_type = 'recovery_job';

CREATE OR REPLACE FUNCTION backup_asset_recovery_latch_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id = '00000000000000000000000000000069' AND OLD.kind = 'schema_use_latch' THEN
        RAISE EXCEPTION 'schema_use_latch is immutable and permanent';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_evidence_latch_update
BEFORE UPDATE ON backup_asset_recovery_evidence
FOR EACH ROW
WHEN (OLD.id = '00000000000000000000000000000069' AND OLD.kind = 'schema_use_latch')
EXECUTE FUNCTION backup_asset_recovery_latch_immutable();
CREATE TRIGGER trg_backup_asset_recovery_evidence_latch_delete
BEFORE DELETE ON backup_asset_recovery_evidence
FOR EACH ROW
WHEN (OLD.id = '00000000000000000000000000000069' AND OLD.kind = 'schema_use_latch')
EXECUTE FUNCTION backup_asset_recovery_latch_immutable();

CREATE OR REPLACE FUNCTION backup_asset_recovery_scheduler_state_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'recovery scheduler state is permanent';
    END IF;
    IF (to_jsonb(NEW) - ARRAY[
            'scheduler_cursor_at', 'scheduler_cursor_id',
            'scheduler_high_water_at', 'scheduler_high_water_id',
            'scheduler_revision', 'updated_at'
        ]) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY[
            'scheduler_cursor_at', 'scheduler_cursor_id',
            'scheduler_high_water_at', 'scheduler_high_water_id',
            'scheduler_revision', 'updated_at'
        ])
       OR NEW.scheduler_revision <> OLD.scheduler_revision + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'recovery scheduler state update is invalid';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_evidence_scheduler_update
BEFORE UPDATE ON backup_asset_recovery_evidence
FOR EACH ROW WHEN (OLD.kind = 'scheduler_state')
EXECUTE FUNCTION backup_asset_recovery_scheduler_state_guard();
CREATE TRIGGER trg_backup_asset_recovery_evidence_scheduler_delete
BEFORE DELETE ON backup_asset_recovery_evidence
FOR EACH ROW WHEN (OLD.kind = 'scheduler_state')
EXECUTE FUNCTION backup_asset_recovery_scheduler_state_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_receipt_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'authorization receipt is immutable';
    END IF;
    IF OLD.replay_expires_at > CURRENT_TIMESTAMP THEN
        RAISE EXCEPTION 'authorization receipt replay window remains active';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_evidence_receipt_update
BEFORE UPDATE ON backup_asset_recovery_evidence
FOR EACH ROW WHEN (OLD.kind = 'authorization_receipt')
EXECUTE FUNCTION backup_asset_recovery_receipt_immutable();
CREATE TRIGGER trg_backup_asset_recovery_evidence_receipt_delete
BEFORE DELETE ON backup_asset_recovery_evidence
FOR EACH ROW WHEN (OLD.kind = 'authorization_receipt')
EXECUTE FUNCTION backup_asset_recovery_receipt_immutable();

CREATE OR REPLACE FUNCTION backup_asset_recovery_receipt_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    source_lease_count BIGINT;
BEGIN
    IF NEW.kind = 'failure' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM backup_asset_recovery_checkpoints AS checkpoint
            JOIN backup_asset_recovery_jobs AS job ON job.id = checkpoint.job_id
            JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
            JOIN backup_asset_recovery_job_items AS item
              ON item.id = checkpoint.job_item_id
             AND item.job_id = job.id
             AND item.plan_id = plan.id
            JOIN backup_asset_recovery_grants AS write_grant
              ON write_grant.id = job.authority_grant_id
            JOIN backup_asset_recovery_attempts AS attempt
              ON attempt.id = checkpoint.attempt_id
             AND attempt.job_id = checkpoint.job_id
            JOIN recovery_point_leases AS source_lease
              ON source_lease.recovery_point_id = plan.recovery_point_id
            JOIN backup_asset_recovery_node_leases AS node_lease
              ON node_lease.job_id = checkpoint.job_id
             AND node_lease.attempt_id = checkpoint.attempt_id
             AND node_lease.node_id = job.target_node_id
            WHERE checkpoint.id = NEW.checkpoint_id
              AND checkpoint.job_id = NEW.job_id
              AND checkpoint.attempt_id = NEW.attempt_id
              AND checkpoint.authority_grant_id = NEW.grant_id
              AND checkpoint.node_fence = NEW.node_lease_fence
              AND job.plan_id = NEW.plan_id
              AND job.state = 'running'
              AND plan.state = 'executed'
              AND (
                  (checkpoint.phase = 'operation_unresolved'
                      AND job.target_chain_revision = checkpoint.prior_target_revision
                      AND item.outcome = 'failed'
                      AND item.failure_category = 'remote_outcome_unresolved')
                  OR (checkpoint.phase = 'operation'
                      AND job.target_chain_revision = checkpoint.next_target_revision
                      AND item.failure_category = ''
                      AND (
                          (item.operation_kind = 'skip' AND item.outcome = 'skipped'
                              AND checkpoint.next_target_revision = checkpoint.prior_target_revision)
                          OR (item.operation_kind IN ('create', 'overwrite', 'delete')
                              AND item.outcome = 'succeeded'
                              AND checkpoint.next_target_revision <> checkpoint.prior_target_revision)
                      ))
              )
              AND write_grant.id = NEW.grant_id
              AND write_grant.plan_id = plan.id
              AND write_grant.job_id IS NULL
              AND write_grant.authority_category = 'write'
              AND write_grant.binding_digest = job.authority_binding_digest
              AND write_grant.consumed_at IS NOT NULL
              AND write_grant.revoked_at IS NULL
              AND attempt.id = NEW.attempt_id
              AND attempt.fence = checkpoint.attempt_fence
              AND attempt.state = 'running'
              AND attempt.mutation_armed
              AND attempt.lease_expires_at > NEW.created_at
              AND attempt.lease_expires_at > CURRENT_TIMESTAMP
              AND source_lease.id = NEW.source_lease_id
              AND source_lease.holder_type = 'recovery_job'
              AND source_lease.owner_id = NEW.job_id
              AND source_lease.attempt_id = NEW.attempt_id
              AND source_lease.fence_token <> ''
              AND source_lease.status = 'active'
              AND source_lease.lease_expires_at > NEW.created_at
              AND source_lease.absolute_deadline > NEW.created_at
              AND source_lease.lease_expires_at > CURRENT_TIMESTAMP
              AND source_lease.absolute_deadline > CURRENT_TIMESTAMP
              AND node_lease.id = NEW.node_lease_id
              AND node_lease.holder_kind = 'recovery_job'
              AND node_lease.owner_id = attempt.owner_id
              AND node_lease.fence = NEW.node_lease_fence
              AND node_lease.state = 'active'
              AND node_lease.lease_expires_at > NEW.created_at
              AND node_lease.lease_expires_at > CURRENT_TIMESTAMP
        ) THEN
            RAISE EXCEPTION 'recovery worker failure evidence binding mismatch';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_plans AS plan_row
        WHERE plan_row.id = NEW.plan_id AND plan_row.requester_id = NEW.requester_id
    ) THEN
        RAISE EXCEPTION 'authorization receipt plan linkage is invalid';
    END IF;
    IF NEW.grant_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_grants AS grant_row
        WHERE grant_row.id = NEW.grant_id
          AND grant_row.plan_id = NEW.plan_id
          AND grant_row.binding_digest = NEW.grant_binding_digest
          AND grant_row.expires_at <= NEW.replay_expires_at
          AND ((NEW.operation = 'write_authorize' AND grant_row.authority_category = 'write' AND grant_row.job_id IS NULL)
            OR (NEW.operation = 'execute' AND grant_row.authority_category = 'write'
                AND grant_row.job_id IS NULL AND grant_row.consumed_at IS NOT NULL
                AND EXISTS (
                    SELECT 1 FROM backup_asset_recovery_jobs AS authority_job
                    WHERE authority_job.id = NEW.job_id AND authority_job.plan_id = NEW.plan_id
                      AND authority_job.authority_grant_id = grant_row.id
                ))
            OR (NEW.operation = 'exact_mirror_delete_authorize'
                AND grant_row.authority_category = 'exact_mirror_delete'
                AND grant_row.job_id = NEW.job_id AND grant_row.delete_checkpoint_id = NEW.checkpoint_id
                AND grant_row.delete_attempt_id = NEW.attempt_id))
    ) THEN
        RAISE EXCEPTION 'authorization receipt grant linkage is invalid';
    END IF;
    IF NEW.job_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs AS job_row
        WHERE job_row.id = NEW.job_id AND job_row.plan_id = NEW.plan_id
    ) THEN
        RAISE EXCEPTION 'authorization receipt job linkage is invalid';
    END IF;
    IF NEW.attempt_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_attempts AS attempt_row
        WHERE attempt_row.id = NEW.attempt_id AND attempt_row.job_id = NEW.job_id
    ) THEN
        RAISE EXCEPTION 'authorization receipt attempt linkage is invalid';
    END IF;
    IF NEW.checkpoint_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_checkpoints AS checkpoint_row
        WHERE checkpoint_row.id = NEW.checkpoint_id AND checkpoint_row.job_id = NEW.job_id
          AND checkpoint_row.attempt_id = NEW.attempt_id
    ) THEN
        RAISE EXCEPTION 'authorization receipt checkpoint linkage is invalid';
    END IF;
    IF NEW.node_lease_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_node_leases AS node_lease
        WHERE node_lease.id = NEW.node_lease_id AND node_lease.job_id = NEW.job_id
          AND node_lease.attempt_id = NEW.attempt_id AND node_lease.fence = NEW.node_lease_fence
    ) THEN
        RAISE EXCEPTION 'authorization receipt node lease linkage is invalid';
    END IF;
    IF NEW.operation = 'execute' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM recovery_point_leases AS source_lease
            JOIN backup_asset_recovery_plans AS plan_row
              ON plan_row.id = NEW.plan_id AND plan_row.recovery_point_id = source_lease.recovery_point_id
            WHERE source_lease.id = NEW.source_lease_id
              AND source_lease.holder_type = 'recovery_job'
              AND source_lease.owner_id = NEW.job_id
              AND source_lease.attempt_id = NEW.attempt_id
        ) THEN
            RAISE EXCEPTION 'authorization receipt source lease linkage is invalid';
        END IF;
        SELECT COUNT(*) INTO source_lease_count
        FROM recovery_point_leases AS source_lease
        WHERE source_lease.holder_type = 'recovery_job'
          AND source_lease.owner_id = NEW.job_id
          AND source_lease.attempt_id = NEW.attempt_id;
        IF source_lease_count <> 1 THEN
            RAISE EXCEPTION 'authorization receipt requires exactly one source lease';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_evidence_receipt_insert
BEFORE INSERT ON backup_asset_recovery_evidence
FOR EACH ROW WHEN (NEW.kind IN ('authorization_receipt', 'failure'))
EXECUTE FUNCTION backup_asset_recovery_receipt_insert_guard();

CREATE TABLE backup_asset_recovery_node_leases (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    holder_kind VARCHAR(32) NOT NULL CHECK (holder_kind IN ('recovery_job', 'recovery_cleanup')),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_jobs(id) ON DELETE CASCADE,
    attempt_id VARCHAR(32) CHECK (attempt_id IS NULL OR attempt_id ~ '^[0-9a-f]{32}$'),
    owner_id VARCHAR(64) NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 64),
    fence BIGINT NOT NULL CHECK (fence > 0),
    state VARCHAR(32) NOT NULL CHECK (state IN ('active', 'released', 'lost', 'expired')),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, job_id),
    CHECK ((holder_kind = 'recovery_job' AND attempt_id IS NOT NULL)
        OR (holder_kind = 'recovery_cleanup' AND attempt_id IS NULL)),
    CHECK ((state = 'active' AND released_at IS NULL)
        OR (state IN ('released', 'lost', 'expired') AND released_at IS NOT NULL)),
    CHECK (lease_expires_at >= created_at AND created_at <= updated_at),
    FOREIGN KEY (job_id, node_id)
        REFERENCES backup_asset_recovery_jobs(id, target_node_id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, job_id) REFERENCES backup_asset_recovery_attempts(id, job_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_node_leases_active_node
    ON backup_asset_recovery_node_leases(node_id) WHERE state = 'active';
CREATE INDEX idx_backup_asset_recovery_node_leases_claim
    ON backup_asset_recovery_node_leases(state, lease_expires_at, node_id);
ALTER TABLE backup_asset_recovery_jobs
    ADD CONSTRAINT backup_asset_recovery_jobs_workspace_cleanup_node_lease_fk
    FOREIGN KEY (workspace_cleanup_node_lease_id, id)
    REFERENCES backup_asset_recovery_node_leases(id, job_id) ON DELETE RESTRICT;
CREATE OR REPLACE FUNCTION backup_asset_recovery_job_workspace_cleanup_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.workspace_cleanup_phase <> 'claimed'
       OR NEW.workspace_cleanup_owner <> ''
       OR NEW.workspace_cleanup_lease_expires_at IS NOT NULL
       OR NEW.workspace_cleanup_fence <> 0
       OR NEW.workspace_cleanup_node_lease_id IS NOT NULL
       OR NEW.workspace_cleanup_node_fence <> 0
       OR NEW.workspace_cleanup_attempt <> 0 THEN
        RAISE EXCEPTION 'recovery workspace cleanup must start neutral';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_jobs_workspace_cleanup_insert
BEFORE INSERT ON backup_asset_recovery_jobs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_workspace_cleanup_insert_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_job_workspace_cleanup_transition_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.workspace_cleanup_phase IS NOT DISTINCT FROM OLD.workspace_cleanup_phase
       AND NEW.workspace_cleanup_owner IS NOT DISTINCT FROM OLD.workspace_cleanup_owner
       AND NEW.workspace_cleanup_lease_expires_at IS NOT DISTINCT FROM OLD.workspace_cleanup_lease_expires_at
       AND NEW.workspace_cleanup_fence IS NOT DISTINCT FROM OLD.workspace_cleanup_fence
       AND NEW.workspace_cleanup_node_lease_id IS NOT DISTINCT FROM OLD.workspace_cleanup_node_lease_id
       AND NEW.workspace_cleanup_node_fence IS NOT DISTINCT FROM OLD.workspace_cleanup_node_fence
       AND NEW.workspace_cleanup_attempt IS NOT DISTINCT FROM OLD.workspace_cleanup_attempt THEN
        RETURN NEW;
    END IF;

    IF OLD.workspace_phase = 'cleanup_due'
       AND NEW.workspace_phase = 'cleanup_due'
       AND OLD.workspace_cleanup_owner = ''
       AND OLD.workspace_cleanup_lease_expires_at IS NULL
       AND OLD.workspace_cleanup_node_lease_id IS NULL
       AND OLD.workspace_cleanup_node_fence = 0
       AND (
            (OLD.workspace_cleanup_phase = 'claimed'
                AND OLD.workspace_cleanup_fence = 0
                AND OLD.workspace_cleanup_attempt = 0
                AND NEW.workspace_cleanup_phase = 'claimed')
            OR (OLD.workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted')
                AND OLD.workspace_cleanup_fence > 0
                AND OLD.workspace_cleanup_attempt > 0
                AND NEW.workspace_cleanup_phase = OLD.workspace_cleanup_phase)
       )
       AND NEW.workspace_cleanup_owner <> ''
       AND NEW.workspace_cleanup_lease_expires_at IS NOT NULL
       AND NEW.workspace_cleanup_fence = OLD.workspace_cleanup_fence + 1
       AND NEW.workspace_cleanup_node_lease_id IS NOT NULL
       AND NEW.workspace_cleanup_node_fence > 0
       AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt + 1
       AND EXISTS (
            SELECT 1 FROM backup_asset_recovery_node_leases AS cleanup_lease
            WHERE cleanup_lease.id = NEW.workspace_cleanup_node_lease_id
              AND cleanup_lease.job_id = OLD.id
              AND cleanup_lease.node_id = OLD.target_node_id
              AND cleanup_lease.holder_kind = 'recovery_cleanup'
              AND cleanup_lease.attempt_id IS NULL
              AND cleanup_lease.owner_id = NEW.workspace_cleanup_owner
              AND cleanup_lease.fence = NEW.workspace_cleanup_node_fence
              AND cleanup_lease.state = 'active'
              AND cleanup_lease.lease_expires_at = NEW.workspace_cleanup_lease_expires_at
       ) THEN
        RETURN NEW;
    END IF;

    IF OLD.workspace_phase = 'cleanup_due'
       AND NEW.workspace_phase = 'cleanup_due'
       AND OLD.workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted')
       AND OLD.workspace_cleanup_owner <> ''
       AND OLD.workspace_cleanup_lease_expires_at IS NOT NULL
       AND OLD.workspace_cleanup_lease_expires_at <= CURRENT_TIMESTAMP
       AND OLD.workspace_cleanup_fence > 0
       AND OLD.workspace_cleanup_node_lease_id IS NOT NULL
       AND OLD.workspace_cleanup_node_fence > 0
       AND OLD.workspace_cleanup_attempt > 0
       AND NEW.workspace_cleanup_phase = OLD.workspace_cleanup_phase
       AND NEW.workspace_cleanup_owner <> ''
       AND NEW.workspace_cleanup_lease_expires_at IS NOT NULL
       AND NEW.workspace_cleanup_fence = OLD.workspace_cleanup_fence + 1
       AND NEW.workspace_cleanup_node_lease_id IS NOT NULL
       AND NEW.workspace_cleanup_node_fence > OLD.workspace_cleanup_node_fence
       AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt + 1
       AND EXISTS (
            SELECT 1 FROM backup_asset_recovery_node_leases AS cleanup_lease
            WHERE cleanup_lease.id = NEW.workspace_cleanup_node_lease_id
              AND cleanup_lease.job_id = OLD.id
              AND cleanup_lease.node_id = OLD.target_node_id
              AND cleanup_lease.holder_kind = 'recovery_cleanup'
              AND cleanup_lease.attempt_id IS NULL
              AND cleanup_lease.owner_id = NEW.workspace_cleanup_owner
              AND cleanup_lease.fence = NEW.workspace_cleanup_node_fence
              AND cleanup_lease.state = 'active'
              AND cleanup_lease.lease_expires_at = NEW.workspace_cleanup_lease_expires_at
       ) THEN
        RETURN NEW;
    END IF;

    IF OLD.workspace_phase = 'cleanup_due'
       AND NEW.workspace_phase = 'cleanup_due'
       AND OLD.workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted')
       AND (
            NEW.workspace_cleanup_phase = OLD.workspace_cleanup_phase
            OR (OLD.workspace_cleanup_phase = 'claimed' AND NEW.workspace_cleanup_phase = 'revoked')
            OR (OLD.workspace_cleanup_phase = 'revoked' AND NEW.workspace_cleanup_phase = 'drained')
            OR (OLD.workspace_cleanup_phase = 'drained' AND NEW.workspace_cleanup_phase = 'validated')
            OR (OLD.workspace_cleanup_phase = 'validated' AND NEW.workspace_cleanup_phase = 'delete_started')
            OR (OLD.workspace_cleanup_phase = 'delete_started' AND NEW.workspace_cleanup_phase = 'deleted')
       )
       AND NEW.workspace_cleanup_owner = OLD.workspace_cleanup_owner
       AND NEW.workspace_cleanup_owner <> ''
       AND NEW.workspace_cleanup_lease_expires_at >= OLD.workspace_cleanup_lease_expires_at
       AND NEW.workspace_cleanup_fence = OLD.workspace_cleanup_fence
       AND NEW.workspace_cleanup_node_lease_id = OLD.workspace_cleanup_node_lease_id
       AND NEW.workspace_cleanup_node_fence = OLD.workspace_cleanup_node_fence
       AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt
       AND EXISTS (
            SELECT 1 FROM backup_asset_recovery_node_leases AS cleanup_lease
            WHERE cleanup_lease.id = NEW.workspace_cleanup_node_lease_id
              AND cleanup_lease.job_id = OLD.id
              AND cleanup_lease.node_id = OLD.target_node_id
              AND cleanup_lease.holder_kind = 'recovery_cleanup'
              AND cleanup_lease.attempt_id IS NULL
              AND cleanup_lease.owner_id = NEW.workspace_cleanup_owner
              AND cleanup_lease.fence = NEW.workspace_cleanup_node_fence
              AND cleanup_lease.state = 'active'
              AND cleanup_lease.lease_expires_at = NEW.workspace_cleanup_lease_expires_at
       ) THEN
        RETURN NEW;
    END IF;

    IF OLD.workspace_phase = 'cleanup_due'
       AND NEW.workspace_phase = 'cleanup_due'
       AND NEW.workspace_cleanup_phase = OLD.workspace_cleanup_phase
       AND OLD.workspace_cleanup_owner <> ''
       AND OLD.workspace_cleanup_lease_expires_at IS NOT NULL
       AND OLD.workspace_cleanup_fence > 0
       AND OLD.workspace_cleanup_node_lease_id IS NOT NULL
       AND OLD.workspace_cleanup_node_fence > 0
       AND OLD.workspace_cleanup_attempt > 0
       AND NEW.workspace_cleanup_owner = ''
       AND NEW.workspace_cleanup_lease_expires_at IS NULL
       AND NEW.workspace_cleanup_fence = OLD.workspace_cleanup_fence
       AND NEW.workspace_cleanup_node_lease_id IS NULL
       AND NEW.workspace_cleanup_node_fence = 0
       AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt THEN
        RETURN NEW;
    END IF;

    IF OLD.workspace_phase = 'cleanup_due'
       AND NEW.workspace_phase = 'workspace_cleaned'
       AND OLD.workspace_cleanup_phase = 'deleted'
       AND NEW.workspace_cleanup_phase = 'tombstoned'
       AND OLD.workspace_cleanup_owner <> ''
       AND OLD.workspace_cleanup_lease_expires_at IS NOT NULL
       AND OLD.workspace_cleanup_fence > 0
       AND OLD.workspace_cleanup_node_lease_id IS NOT NULL
       AND OLD.workspace_cleanup_node_fence > 0
       AND OLD.workspace_cleanup_attempt > 0
       AND NEW.workspace_cleanup_owner = ''
       AND NEW.workspace_cleanup_lease_expires_at IS NULL
       AND NEW.workspace_cleanup_fence = OLD.workspace_cleanup_fence
       AND NEW.workspace_cleanup_node_lease_id IS NULL
       AND NEW.workspace_cleanup_node_fence = 0
       AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'recovery workspace cleanup transition is invalid';
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_jobs_workspace_cleanup_transition
BEFORE UPDATE OF workspace_cleanup_phase, workspace_cleanup_owner,
    workspace_cleanup_lease_expires_at, workspace_cleanup_fence,
    workspace_cleanup_node_lease_id, workspace_cleanup_node_fence,
    workspace_cleanup_attempt
ON backup_asset_recovery_jobs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_workspace_cleanup_transition_guard();
ALTER TABLE backup_asset_recovery_evidence
    ADD CONSTRAINT backup_asset_recovery_evidence_node_lease_fk
    FOREIGN KEY (node_lease_id) REFERENCES backup_asset_recovery_node_leases(id) ON DELETE RESTRICT;
CREATE TABLE backup_asset_recovery_result_sets (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL UNIQUE CHECK (job_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_jobs(id) ON DELETE RESTRICT,
    state VARCHAR(32) NOT NULL CHECK (state IN ('ready', 'revoking', 'cleaned', 'cleanup_failed')),
    marker_binding_digest VARCHAR(64) NOT NULL CHECK (marker_binding_digest ~ '^[0-9a-f]{64}$'),
    plaintext_deadline TIMESTAMPTZ NOT NULL,
    hard_deadline TIMESTAMPTZ NOT NULL,
    cleanup_phase VARCHAR(32) NOT NULL CHECK (cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted', 'tombstoned')),
    cleanup_owner VARCHAR(64) NOT NULL DEFAULT '',
    cleanup_lease_expires_at TIMESTAMPTZ,
    cleanup_fence BIGINT NOT NULL DEFAULT 0 CHECK (cleanup_fence >= 0),
    node_lease_id VARCHAR(32) CHECK (node_lease_id IS NULL OR node_lease_id ~ '^[0-9a-f]{32}$'),
    node_fence BIGINT NOT NULL DEFAULT 0 CHECK (node_fence >= 0),
    cleanup_attempt BIGINT NOT NULL DEFAULT 0 CHECK (cleanup_attempt >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, job_id),
    CHECK (created_at < plaintext_deadline AND plaintext_deadline <= hard_deadline),
    CHECK (
        (state = 'ready' AND cleanup_owner = '' AND cleanup_lease_expires_at IS NULL
            AND cleanup_fence = 0 AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt = 0 AND cleanup_phase = 'claimed')
        OR (state = 'revoking' AND cleanup_owner <> '' AND cleanup_lease_expires_at IS NOT NULL
            AND cleanup_fence > 0 AND node_lease_id IS NOT NULL AND node_fence > 0 AND cleanup_attempt > 0)
        OR (state = 'cleanup_failed' AND cleanup_owner = '' AND cleanup_lease_expires_at IS NULL
            AND cleanup_fence > 0 AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt > 0)
        OR (state = 'cleaned' AND cleanup_phase = 'tombstoned' AND cleanup_owner = ''
            AND cleanup_lease_expires_at IS NULL AND cleanup_fence > 0
            AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt > 0)
    ),
    CHECK (created_at <= updated_at),
    FOREIGN KEY (node_lease_id, job_id) REFERENCES backup_asset_recovery_node_leases(id, job_id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_recovery_result_sets_expiry
    ON backup_asset_recovery_result_sets(state, plaintext_deadline, hard_deadline);
CREATE INDEX idx_backup_asset_recovery_result_sets_cleanup
    ON backup_asset_recovery_result_sets(state, cleanup_lease_expires_at, updated_at);

CREATE TABLE backup_asset_recovery_results (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    result_set_id VARCHAR(32) NOT NULL CHECK (result_set_id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    result_kind VARCHAR(16) NOT NULL CHECK (result_kind IN ('regular_file', 'verification_report')),
    classification VARCHAR(16) NOT NULL CHECK (classification IN ('non_secret', 'secret', 'unknown')),
    classification_revision INTEGER NOT NULL CHECK (classification_revision > 0),
    classification_source_revision BIGINT NOT NULL CHECK (classification_source_revision > 0),
    encrypted_relative_locator TEXT NOT NULL DEFAULT '',
    locator_digest VARCHAR(64) NOT NULL CHECK (locator_digest ~ '^[0-9a-f]{64}$'),
    size BIGINT NOT NULL DEFAULT 0 CHECK (size >= 0),
    content_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (content_digest = ''
        OR content_digest ~ '^[0-9a-f]{64}$'),
    modified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (id, job_id),
    UNIQUE (result_set_id, locator_digest),
    CHECK (encrypted_relative_locator <> ''),
    FOREIGN KEY (result_set_id, job_id)
        REFERENCES backup_asset_recovery_result_sets(id, job_id) ON DELETE CASCADE
);
CREATE INDEX idx_backup_asset_recovery_results_job
    ON backup_asset_recovery_results(job_id, id);

CREATE TABLE backup_asset_recovery_grants (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plan_id VARCHAR(32) NOT NULL CHECK (plan_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_recovery_plans(id) ON DELETE CASCADE,
    job_id VARCHAR(32) CHECK (job_id IS NULL OR job_id ~ '^[0-9a-f]{32}$'),
    authority_category VARCHAR(32) NOT NULL CHECK (authority_category IN ('write', 'exact_mirror_delete')),
    grant_hash VARCHAR(64) NOT NULL CHECK (grant_hash ~ '^[0-9a-f]{64}$'),
    actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    actor_session_id VARCHAR(64) NOT NULL CHECK (length(actor_session_id) BETWEEN 1 AND 64),
    binding_digest VARCHAR(64) NOT NULL CHECK (binding_digest ~ '^[0-9a-f]{64}$'),
    encrypted_reason TEXT NOT NULL DEFAULT '',
    delete_checkpoint_id VARCHAR(32) CHECK (delete_checkpoint_id IS NULL OR delete_checkpoint_id ~ '^[0-9a-f]{32}$'),
    delete_set_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (delete_set_digest = '' OR delete_set_digest ~ '^[0-9a-f]{64}$'),
    delete_target_revision VARCHAR(64) NOT NULL DEFAULT '',
    delete_attempt_id VARCHAR(32) CHECK (delete_attempt_id IS NULL OR delete_attempt_id ~ '^[0-9a-f]{32}$'),
    delete_attempt_fence BIGINT NOT NULL DEFAULT 0 CHECK (delete_attempt_fence >= 0),
    delete_node_fence BIGINT NOT NULL DEFAULT 0 CHECK (delete_node_fence >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK ((authority_category = 'write' AND job_id IS NULL AND delete_checkpoint_id IS NULL
            AND delete_set_digest = '' AND delete_target_revision = '' AND delete_attempt_id IS NULL
            AND delete_attempt_fence = 0 AND delete_node_fence = 0)
        OR (authority_category = 'exact_mirror_delete' AND job_id IS NOT NULL
            AND delete_checkpoint_id IS NOT NULL AND length(delete_set_digest) = 64
            AND delete_target_revision <> '' AND delete_attempt_id IS NOT NULL
            AND delete_attempt_fence > 0 AND delete_node_fence > 0)),
    CHECK (encrypted_reason <> '' AND expires_at > created_at AND created_at <= updated_at),
    CHECK (consumed_at IS NULL OR (consumed_at >= created_at AND consumed_at <= expires_at)),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CHECK (consumed_at IS NULL OR revoked_at IS NULL),
    FOREIGN KEY (job_id, plan_id) REFERENCES backup_asset_recovery_jobs(id, plan_id) ON DELETE RESTRICT,
    FOREIGN KEY (delete_checkpoint_id) REFERENCES backup_asset_recovery_checkpoints(id) ON DELETE RESTRICT,
    FOREIGN KEY (delete_attempt_id, job_id) REFERENCES backup_asset_recovery_attempts(id, job_id) ON DELETE RESTRICT
);
ALTER TABLE backup_asset_recovery_evidence
    ADD CONSTRAINT backup_asset_recovery_evidence_grant_fk
    FOREIGN KEY (grant_id) REFERENCES backup_asset_recovery_grants(id) ON DELETE RESTRICT;
INSERT INTO backup_asset_recovery_evidence
    (id, kind, scheduler_scope, scheduler_revision, created_at, updated_at)
VALUES
    ('0000000000000000000000000000006a', 'scheduler_state', 'claim', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('0000000000000000000000000000006b', 'scheduler_state', 'takeover', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
CREATE INDEX idx_backup_asset_recovery_grants_plan_category_expiry
    ON backup_asset_recovery_grants(plan_id, authority_category, expires_at);
CREATE INDEX idx_backup_asset_recovery_grants_job_category
    ON backup_asset_recovery_grants(job_id, authority_category, consumed_at);

CREATE OR REPLACE FUNCTION backup_asset_recovery_grant_terminal_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF EXISTS (
            SELECT 1 FROM backup_asset_recovery_grants
            WHERE id = NEW.id AND (consumed_at IS NOT NULL OR revoked_at IS NOT NULL)
        ) THEN
            RAISE EXCEPTION 'terminal recovery grant cannot be replaced';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.consumed_at IS NOT NULL OR OLD.revoked_at IS NOT NULL THEN
            RAISE EXCEPTION 'terminal recovery grant cannot be deleted';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.consumed_at IS NOT NULL OR OLD.revoked_at IS NOT NULL THEN
        RAISE EXCEPTION 'recovery grant terminal state is immutable';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.plan_id IS DISTINCT FROM OLD.plan_id
        OR NEW.job_id IS DISTINCT FROM OLD.job_id
        OR NEW.authority_category IS DISTINCT FROM OLD.authority_category
        OR NEW.grant_hash IS DISTINCT FROM OLD.grant_hash
        OR NEW.actor_user_id IS DISTINCT FROM OLD.actor_user_id
        OR NEW.actor_session_id IS DISTINCT FROM OLD.actor_session_id
        OR NEW.binding_digest IS DISTINCT FROM OLD.binding_digest
        OR NEW.encrypted_reason IS DISTINCT FROM OLD.encrypted_reason
        OR NEW.delete_checkpoint_id IS DISTINCT FROM OLD.delete_checkpoint_id
        OR NEW.delete_set_digest IS DISTINCT FROM OLD.delete_set_digest
        OR NEW.delete_target_revision IS DISTINCT FROM OLD.delete_target_revision
        OR NEW.delete_attempt_id IS DISTINCT FROM OLD.delete_attempt_id
        OR NEW.delete_attempt_fence IS DISTINCT FROM OLD.delete_attempt_fence
        OR NEW.delete_node_fence IS DISTINCT FROM OLD.delete_node_fence
        OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'recovery grant terminal state is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_grants_terminal
BEFORE UPDATE ON backup_asset_recovery_grants
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_grant_terminal_guard();

CREATE TRIGGER trg_backup_asset_recovery_grants_terminal_delete
BEFORE DELETE ON backup_asset_recovery_grants
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_grant_terminal_guard();

CREATE TRIGGER trg_backup_asset_recovery_grants_terminal_replay
BEFORE INSERT ON backup_asset_recovery_grants
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_grant_terminal_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_grant_delete_binding_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.authority_category = 'exact_mirror_delete' AND NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_checkpoints AS required
        JOIN backup_asset_recovery_jobs AS job ON job.id = required.job_id
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        JOIN backup_asset_recovery_attempts AS attempt
          ON attempt.id = required.attempt_id AND attempt.job_id = required.job_id
        JOIN backup_asset_recovery_node_leases AS node_lease
          ON node_lease.job_id = required.job_id
         AND node_lease.attempt_id = required.attempt_id
        WHERE required.id = NEW.delete_checkpoint_id
          AND required.phase = 'delete_authority_required'
          AND required.authority_category = 'exact_mirror_delete'
          AND required.job_id = NEW.job_id
          AND required.attempt_id = NEW.delete_attempt_id
          AND required.operation_digest = NEW.delete_set_digest
          AND required.prior_target_revision = NEW.delete_target_revision
          AND required.attempt_fence = NEW.delete_attempt_fence
          AND required.node_fence = NEW.delete_node_fence
          AND required.delete_root_revision = plan.root_revision
          AND required.created_at <= NEW.created_at
          AND required.delete_authority_expires_at > NEW.created_at
          AND NEW.expires_at <= required.delete_authority_expires_at
          AND job.plan_id = NEW.plan_id
          AND job.state = 'running'
          AND job.target_mode = 'in_place'
          AND job.delete_set_digest = NEW.delete_set_digest
          AND job.target_chain_revision = NEW.delete_target_revision
          AND plan.target_mode = 'in_place'
          AND plan.conflict_policy = 'exact_mirror'
          AND attempt.fence = NEW.delete_attempt_fence
          AND attempt.state IN ('claimed', 'running')
          AND attempt.lease_expires_at > NEW.created_at
          AND node_lease.fence = NEW.delete_node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
    ) THEN
        RAISE EXCEPTION 'exact-mirror delete grant binding mismatch';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_grants_delete_binding_insert
BEFORE INSERT ON backup_asset_recovery_grants
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_grant_delete_binding_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_frozen_product_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME;
END;
$$;

CREATE OR REPLACE FUNCTION backup_asset_recovery_job_item_insert_binding_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_jobs AS job
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        WHERE job.id = NEW.job_id
          AND job.plan_id = NEW.plan_id
          AND (
              NEW.operation_kind <> 'delete'
              OR (job.target_mode = 'in_place' AND plan.conflict_policy = 'exact_mirror')
          )
    ) THEN
        RAISE EXCEPTION 'recovery job item insert binding mismatch';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_job_items_insert_binding
BEFORE INSERT ON backup_asset_recovery_job_items
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_item_insert_binding_guard();

CREATE TRIGGER trg_backup_asset_recovery_job_items_binding_immutable
BEFORE UPDATE OF
    id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
    semantic_target_digest, target_object_digest,
    expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
    expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
    target_locator_key_version, target_locator_cipher_version, display_class,
    estimated_bytes, created_at
ON backup_asset_recovery_job_items
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_frozen_product_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_job_item_projection_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.outcome <> '' OR NEW.outcome = '' THEN
        RAISE EXCEPTION 'recovery job item permits only pending-to-terminal projection';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_backup_asset_recovery_job_items_projection
BEFORE UPDATE ON backup_asset_recovery_job_items
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_item_projection_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_plan_binding_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (
            OLD.state IN ('authorized', 'executed')
            OR OLD.security_decision = 'admin_override'
            OR EXISTS (SELECT 1 FROM backup_asset_recovery_grants WHERE plan_id = OLD.id)
            OR EXISTS (SELECT 1 FROM backup_asset_recovery_jobs WHERE plan_id = OLD.id)
        ) AND (
            NEW.id IS DISTINCT FROM OLD.id
            OR NEW.requester_id IS DISTINCT FROM OLD.requester_id
            OR NEW.endpoint IS DISTINCT FROM OLD.endpoint
            OR NEW.idempotency_key_digest IS DISTINCT FROM OLD.idempotency_key_digest
            OR NEW.repository_id IS DISTINCT FROM OLD.repository_id
            OR NEW.recovery_point_id IS DISTINCT FROM OLD.recovery_point_id
            OR NEW.source_revision_digest IS DISTINCT FROM OLD.source_revision_digest
            OR NEW.source_revision_kind IS DISTINCT FROM OLD.source_revision_kind
            OR NEW.immutable_locator_digest IS DISTINCT FROM OLD.immutable_locator_digest
            OR NEW.immutable_manifest_digest IS DISTINCT FROM OLD.immutable_manifest_digest
            OR NEW.observation_fingerprint IS DISTINCT FROM OLD.observation_fingerprint
            OR NEW.catalog_generation_id IS DISTINCT FROM OLD.catalog_generation_id
            OR NEW.observed_at IS DISTINCT FROM OLD.observed_at
            OR NEW.encrypted_source_locator IS DISTINCT FROM OLD.encrypted_source_locator
            OR NEW.target_mode IS DISTINCT FROM OLD.target_mode
            OR NEW.target_node_id IS DISTINCT FROM OLD.target_node_id
            OR NEW.target_root_id IS DISTINCT FROM OLD.target_root_id
            OR NEW.encrypted_target_root_locator IS DISTINCT FROM OLD.encrypted_target_root_locator
            OR NEW.encrypted_target_relative_path IS DISTINCT FROM OLD.encrypted_target_relative_path
            OR NEW.root_locator_digest IS DISTINCT FROM OLD.root_locator_digest
            OR NEW.path_digest IS DISTINCT FROM OLD.path_digest
            OR NEW.target_base_revision IS DISTINCT FROM OLD.target_base_revision
            OR NEW.credential_scope_revision IS DISTINCT FROM OLD.credential_scope_revision
            OR NEW.root_revision IS DISTINCT FROM OLD.root_revision
            OR NEW.filesystem_revision IS DISTINCT FROM OLD.filesystem_revision
            OR NEW.selection_digest IS DISTINCT FROM OLD.selection_digest
            OR NEW.binding_digest IS DISTINCT FROM OLD.binding_digest
            OR NEW.capability_revision IS DISTINCT FROM OLD.capability_revision
            OR NEW.conflict_policy IS DISTINCT FROM OLD.conflict_policy
            OR NEW.operation_set_digest IS DISTINCT FROM OLD.operation_set_digest
            OR NEW.delete_set_digest IS DISTINCT FROM OLD.delete_set_digest
            OR NEW.security_decision IS DISTINCT FROM OLD.security_decision
            OR NEW.security_decision_digest IS DISTINCT FROM OLD.security_decision_digest
            OR NEW.security_finding_set_digest IS DISTINCT FROM OLD.security_finding_set_digest
            OR NEW.security_policy_revision IS DISTINCT FROM OLD.security_policy_revision
            OR NEW.security_override_binding_digest IS DISTINCT FROM OLD.security_override_binding_digest
            OR NEW.encrypted_override_reason IS DISTINCT FROM OLD.encrypted_override_reason
            OR NEW.preflight_revision IS DISTINCT FROM OLD.preflight_revision
            OR NEW.preflight_expires_at IS DISTINCT FROM OLD.preflight_expires_at
            OR NEW.estimated_items IS DISTINCT FROM OLD.estimated_items
            OR NEW.estimated_bytes IS DISTINCT FROM OLD.estimated_bytes
            OR NEW.created_at IS DISTINCT FROM OLD.created_at
        ) THEN
        RAISE EXCEPTION 'authorized recovery plan binding is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_backup_asset_recovery_plans_binding_frozen
BEFORE UPDATE OF
    id, requester_id, endpoint, idempotency_key_digest, repository_id, recovery_point_id,
    source_revision_digest, source_revision_kind, immutable_locator_digest, immutable_manifest_digest,
    observation_fingerprint, catalog_generation_id, observed_at, encrypted_source_locator,
    target_mode, target_node_id, target_root_id, encrypted_target_root_locator,
    encrypted_target_relative_path, root_locator_digest, path_digest, target_base_revision,
    credential_scope_revision, root_revision, filesystem_revision, selection_digest, binding_digest,
    capability_revision, conflict_policy, operation_set_digest, delete_set_digest, security_decision,
    security_decision_digest, security_finding_set_digest, security_policy_revision,
    security_override_binding_digest, encrypted_override_reason, preflight_revision,
    preflight_expires_at, estimated_items, estimated_bytes, created_at
ON backup_asset_recovery_plans
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_plan_binding_guard();

CREATE TRIGGER trg_backup_asset_recovery_preflights_immutable
BEFORE UPDATE ON backup_asset_recovery_preflights
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_frozen_product_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_job_authority_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_plans AS plan
        JOIN backup_asset_recovery_preflights AS preflight
          ON preflight.id = NEW.preflight_id AND preflight.plan_id = plan.id
        JOIN backup_asset_recovery_grants AS authority
          ON authority.id = NEW.authority_grant_id AND authority.plan_id = plan.id
        WHERE plan.id = NEW.plan_id
          AND NEW.plan_binding_digest = plan.binding_digest
          AND NEW.selection_digest = plan.selection_digest
          AND NEW.source_revision_digest = plan.source_revision_digest
          AND NEW.source_revision_digest = preflight.source_revision_digest
          AND NEW.preflight_revision = plan.preflight_revision
          AND NEW.preflight_revision = preflight.revision
          AND NEW.preflight_expires_at = plan.preflight_expires_at
          AND NEW.preflight_expires_at = preflight.expires_at
          AND NEW.preflight_target_revision = preflight.target_revision
          AND NEW.preflight_node_revision = plan.target_base_revision
          AND NEW.preflight_node_revision = preflight.node_revision
          AND NEW.target_node_id = plan.target_node_id
          AND NEW.target_node_id = preflight.target_node_id
          AND NEW.target_root_id = plan.target_root_id
          AND NEW.target_root_id = preflight.target_root_id
          AND NEW.root_locator_digest = plan.root_locator_digest
          AND NEW.root_locator_digest = preflight.root_locator_digest
          AND NEW.path_digest = plan.path_digest
          AND NEW.path_digest = preflight.path_digest
          AND NEW.capability_revision = plan.capability_revision
          AND NEW.capability_revision = preflight.capability_revision
          AND NEW.operation_set_digest = plan.operation_set_digest
          AND NEW.operation_set_digest = preflight.operation_set_digest
          AND NEW.delete_set_digest = plan.delete_set_digest
          AND NEW.delete_set_digest = preflight.delete_set_digest
          AND NEW.security_decision = plan.security_decision
          AND NEW.security_decision_digest = plan.security_decision_digest
          AND NEW.security_finding_set_digest = plan.security_finding_set_digest
          AND NEW.security_finding_set_digest = preflight.finding_set_digest
          AND NEW.security_policy_revision = plan.security_policy_revision
          AND NEW.security_policy_revision = preflight.policy_revision
          AND NEW.security_override_binding_digest = plan.security_override_binding_digest
          AND NEW.estimated_items = plan.estimated_items
          AND NEW.estimated_items = preflight.estimated_items
          AND NEW.estimated_bytes = plan.estimated_bytes
          AND NEW.estimated_bytes = preflight.estimated_bytes
          AND NEW.authority_category = 'write'
          AND authority.authority_category = NEW.authority_category
          AND authority.binding_digest = NEW.authority_binding_digest
          AND authority.expires_at = NEW.authority_expires_at
          AND authority.consumed_at = NEW.authority_consumed_at
          AND authority.consumed_at IS NOT NULL
          AND authority.revoked_at IS NULL
    ) THEN
        RAISE EXCEPTION 'recovery job authority binding mismatch';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_jobs_authority_insert
BEFORE INSERT ON backup_asset_recovery_jobs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_authority_insert_guard();

CREATE TRIGGER trg_backup_asset_recovery_jobs_binding_immutable
BEFORE UPDATE OF
    id, plan_id, plan_binding_digest, selection_digest, source_revision_digest,
    preflight_id, preflight_revision, preflight_expires_at, preflight_target_revision, preflight_node_revision,
    capability_revision, operation_set_digest, delete_set_digest, security_decision,
    security_decision_digest, security_finding_set_digest, security_policy_revision,
    security_override_binding_digest, estimated_items, estimated_bytes, authority_grant_id,
    authority_category, authority_binding_digest, authority_expires_at, authority_consumed_at,
    encrypted_workspace_relative_locator, workspace_binding_digest,
    target_mode, target_node_id, target_root_id, root_locator_digest, path_digest, created_at
ON backup_asset_recovery_jobs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_frozen_product_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_checkpoint_authority_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM backup_asset_recovery_checkpoints AS terminal
        WHERE terminal.job_id = NEW.job_id
          AND terminal.phase = 'operation_unresolved'
    ) THEN
        RAISE EXCEPTION 'unresolved operation checkpoint is terminal';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs AS job
        WHERE job.id = NEW.job_id
          AND NEW.plan_binding_digest = job.plan_binding_digest
          AND NEW.source_revision_digest = job.source_revision_digest
          AND NEW.preflight_id = job.preflight_id
          AND NEW.preflight_revision = job.preflight_revision
          AND NEW.preflight_expires_at = job.preflight_expires_at
          AND NEW.security_decision = job.security_decision
          AND NEW.security_decision_digest = job.security_decision_digest
          AND NEW.security_finding_set_digest = job.security_finding_set_digest
          AND NEW.security_policy_revision = job.security_policy_revision
          AND NEW.authority_grant_id = job.authority_grant_id
          AND NEW.job_authority_category = job.authority_category
          AND NEW.authority_binding_digest = job.authority_binding_digest
          AND NEW.authority_expires_at = job.authority_expires_at
    ) THEN
        RAISE EXCEPTION 'recovery checkpoint authority binding mismatch';
    END IF;
    IF NEW.phase = 'operation' AND NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_jobs AS job
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        JOIN backup_asset_recovery_job_items AS item
          ON item.id = NEW.job_item_id
         AND item.job_id = NEW.job_id
         AND item.plan_id = job.plan_id
        JOIN backup_asset_recovery_attempts AS attempt
          ON attempt.id = NEW.attempt_id
         AND attempt.job_id = NEW.job_id
        JOIN backup_asset_recovery_node_leases AS node_lease
          ON node_lease.job_id = NEW.job_id
         AND node_lease.attempt_id = NEW.attempt_id
         AND node_lease.node_id = job.target_node_id
        WHERE job.id = NEW.job_id
          AND job.state = 'running'
          AND job.target_chain_revision = NEW.prior_target_revision
          AND plan.state = 'executed'
          AND item.failure_category = ''
          AND (
              (item.operation_kind = 'skip'
                  AND item.outcome IN ('', 'skipped')
                  AND NEW.next_target_revision = NEW.prior_target_revision)
              OR (item.operation_kind IN ('create', 'overwrite', 'delete')
                  AND item.outcome IN ('', 'succeeded')
                  AND NEW.next_target_revision <> NEW.prior_target_revision)
          )
          AND attempt.fence = NEW.attempt_fence
          AND attempt.state = 'running'
          AND attempt.mutation_armed
          AND attempt.lease_expires_at > NEW.created_at
          AND attempt.lease_expires_at > CURRENT_TIMESTAMP
          AND node_lease.holder_kind = 'recovery_job'
          AND node_lease.owner_id = attempt.owner_id
          AND node_lease.fence = NEW.node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
          AND node_lease.lease_expires_at > CURRENT_TIMESTAMP
          AND NEW.sequence = COALESCE((
              SELECT MAX(existing.sequence) + 1
              FROM backup_asset_recovery_checkpoints AS existing
              WHERE existing.job_id = NEW.job_id
          ), 0)
          AND (
              (NEW.sequence = 0 AND job.target_mode = 'in_place')
              OR (NEW.sequence > 0 AND EXISTS (
                  SELECT 1
                  FROM backup_asset_recovery_checkpoints AS predecessor
                  WHERE predecessor.job_id = NEW.job_id
                    AND predecessor.sequence = NEW.sequence - 1
                    AND predecessor.phase IN (
                        'workspace_reserved', 'operation', 'delete_authority_consumed'
                    )
              ))
          )
    ) THEN
        RAISE EXCEPTION 'operation checkpoint binding mismatch';
    END IF;
    IF NEW.phase = 'operation_unresolved' AND NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_jobs AS job
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        JOIN backup_asset_recovery_job_items AS item
          ON item.id = NEW.job_item_id
         AND item.job_id = NEW.job_id
         AND item.plan_id = job.plan_id
        JOIN backup_asset_recovery_attempts AS attempt
          ON attempt.id = NEW.attempt_id
         AND attempt.job_id = NEW.job_id
        JOIN backup_asset_recovery_node_leases AS node_lease
          ON node_lease.job_id = NEW.job_id
         AND node_lease.attempt_id = NEW.attempt_id
         AND node_lease.node_id = job.target_node_id
        WHERE job.id = NEW.job_id
          AND job.state = 'running'
          AND job.target_chain_revision = NEW.prior_target_revision
          AND plan.state = 'executed'
          AND item.outcome = ''
          AND item.failure_category = ''
          AND (
              (NEW.unresolved_category = 'write_result_invalid'
                  AND item.operation_kind IN ('create', 'overwrite', 'delete')
                  AND (length(NEW.write_result_digest) = 64
                      OR NEW.write_result_digest = '')
                  AND NEW.write_target_revision = ''
                  AND NEW.observation_digest = ''
                  AND NEW.observed_target_revision = ''
                  AND NEW.observed_presence = '')
              OR (NEW.unresolved_category = 'observation_invalid'
                  AND (length(NEW.observation_digest) = 64
                      OR NEW.observation_digest = '')
                  AND NEW.observed_target_revision = ''
                  AND NEW.observed_presence = ''
                  AND (
                      (item.operation_kind = 'skip'
                          AND NEW.write_result_digest = ''
                          AND NEW.write_target_revision = '')
                      OR (item.operation_kind IN ('create', 'overwrite')
                          AND NEW.write_result_digest = ''
                          AND NEW.write_target_revision = '')
                      OR (item.operation_kind IN ('create', 'overwrite', 'delete')
                          AND length(NEW.write_result_digest) = 64
                          AND NEW.write_target_revision <> '')
                      OR (item.operation_kind = 'delete'
                          AND NEW.write_result_digest = ''
                          AND NEW.write_target_revision = ''
                          AND EXISTS (
                              SELECT 1
                              FROM backup_asset_recovery_checkpoints AS consumed
                              JOIN backup_asset_recovery_grants AS delete_grant
                                ON delete_grant.id = consumed.delete_grant_id
                              JOIN backup_asset_recovery_checkpoints AS required
                                ON required.id = delete_grant.delete_checkpoint_id
                              WHERE consumed.phase = 'delete_authority_consumed'
                                AND consumed.authority_category = 'exact_mirror_delete'
                                AND consumed.job_id = NEW.job_id
                                AND consumed.sequence < NEW.sequence
                                AND consumed.operation_digest = job.delete_set_digest
                                AND consumed.attempt_id = NEW.attempt_id
                                AND consumed.attempt_fence = NEW.attempt_fence
                                AND consumed.node_fence = NEW.node_fence
                                AND delete_grant.authority_category = 'exact_mirror_delete'
                                AND delete_grant.plan_id = job.plan_id
                                AND delete_grant.job_id = consumed.job_id
                                AND delete_grant.delete_checkpoint_id = required.id
                                AND delete_grant.delete_set_digest = consumed.operation_digest
                                AND delete_grant.delete_target_revision = consumed.prior_target_revision
                                AND delete_grant.delete_attempt_id = consumed.attempt_id
                                AND delete_grant.delete_attempt_fence = consumed.attempt_fence
                                AND delete_grant.delete_node_fence = consumed.node_fence
                                AND delete_grant.binding_digest = consumed.delete_grant_binding_digest
                                AND delete_grant.expires_at = consumed.delete_grant_expires_at
                                AND delete_grant.consumed_at = consumed.delete_grant_consumed_at
                                AND delete_grant.consumed_at = consumed.created_at
                                AND delete_grant.expires_at > consumed.created_at
                                AND delete_grant.revoked_at IS NULL
                                AND required.phase = 'delete_authority_required'
                                AND required.sequence + 1 = consumed.sequence
                                AND required.job_id = consumed.job_id
                                AND required.attempt_id = consumed.attempt_id
                                AND required.operation_digest = consumed.operation_digest
                                AND required.prior_target_revision = consumed.prior_target_revision
                                AND required.node_fence = consumed.node_fence
                                AND required.attempt_fence = consumed.attempt_fence
                                AND required.delete_node_revision = consumed.delete_node_revision
                                AND required.delete_root_revision = consumed.delete_root_revision
                                AND required.delete_authority_expires_at = consumed.delete_authority_expires_at
                                AND required.delete_authority_expires_at >= delete_grant.expires_at
                                AND job.target_mode = 'in_place'
                                AND plan.target_mode = 'in_place'
                                AND plan.conflict_policy = 'exact_mirror'
                          ))
                  ))
              OR (NEW.unresolved_category = 'revision_disagreement'
                  AND item.operation_kind IN ('create', 'overwrite', 'delete')
                  AND length(NEW.write_result_digest) = 64
                  AND NEW.write_target_revision <> ''
                  AND length(NEW.observation_digest) = 64
                  AND NEW.observed_target_revision <> ''
                  AND NEW.observed_presence <> ''
                  AND NEW.write_target_revision <> NEW.observed_target_revision)
              OR (NEW.unresolved_category = 'verification_mismatch'
                  AND length(NEW.observation_digest) = 64
                  AND NEW.observed_target_revision <> ''
                  AND NEW.observed_presence <> ''
                  AND (
                      (item.operation_kind = 'skip'
                          AND NEW.write_result_digest = ''
                          AND NEW.write_target_revision = '')
                      OR (item.operation_kind IN ('create', 'overwrite')
                          AND NEW.write_result_digest = ''
                          AND NEW.write_target_revision = '')
                      OR (item.operation_kind IN ('create', 'overwrite', 'delete')
                          AND length(NEW.write_result_digest) = 64
                          AND NEW.write_target_revision = NEW.observed_target_revision)
                      OR (item.operation_kind = 'delete'
                          AND NEW.write_result_digest = ''
                          AND NEW.write_target_revision = ''
                          AND EXISTS (
                              SELECT 1
                              FROM backup_asset_recovery_checkpoints AS consumed
                              JOIN backup_asset_recovery_grants AS delete_grant
                                ON delete_grant.id = consumed.delete_grant_id
                              JOIN backup_asset_recovery_checkpoints AS required
                                ON required.id = delete_grant.delete_checkpoint_id
                              WHERE consumed.phase = 'delete_authority_consumed'
                                AND consumed.authority_category = 'exact_mirror_delete'
                                AND consumed.job_id = NEW.job_id
                                AND consumed.sequence < NEW.sequence
                                AND consumed.operation_digest = job.delete_set_digest
                                AND consumed.attempt_id = NEW.attempt_id
                                AND consumed.attempt_fence = NEW.attempt_fence
                                AND consumed.node_fence = NEW.node_fence
                                AND delete_grant.authority_category = 'exact_mirror_delete'
                                AND delete_grant.plan_id = job.plan_id
                                AND delete_grant.job_id = consumed.job_id
                                AND delete_grant.delete_checkpoint_id = required.id
                                AND delete_grant.delete_set_digest = consumed.operation_digest
                                AND delete_grant.delete_target_revision = consumed.prior_target_revision
                                AND delete_grant.delete_attempt_id = consumed.attempt_id
                                AND delete_grant.delete_attempt_fence = consumed.attempt_fence
                                AND delete_grant.delete_node_fence = consumed.node_fence
                                AND delete_grant.binding_digest = consumed.delete_grant_binding_digest
                                AND delete_grant.expires_at = consumed.delete_grant_expires_at
                                AND delete_grant.consumed_at = consumed.delete_grant_consumed_at
                                AND delete_grant.consumed_at = consumed.created_at
                                AND delete_grant.expires_at > consumed.created_at
                                AND delete_grant.revoked_at IS NULL
                                AND required.phase = 'delete_authority_required'
                                AND required.sequence + 1 = consumed.sequence
                                AND required.job_id = consumed.job_id
                                AND required.attempt_id = consumed.attempt_id
                                AND required.operation_digest = consumed.operation_digest
                                AND required.prior_target_revision = consumed.prior_target_revision
                                AND required.node_fence = consumed.node_fence
                                AND required.attempt_fence = consumed.attempt_fence
                                AND required.delete_node_revision = consumed.delete_node_revision
                                AND required.delete_root_revision = consumed.delete_root_revision
                                AND required.delete_authority_expires_at = consumed.delete_authority_expires_at
                                AND required.delete_authority_expires_at >= delete_grant.expires_at
                                AND job.target_mode = 'in_place'
                                AND plan.target_mode = 'in_place'
                                AND plan.conflict_policy = 'exact_mirror'
                          ))
                  ))
          )
          AND attempt.fence = NEW.attempt_fence
          AND attempt.state = 'running'
          AND attempt.mutation_armed
          AND attempt.lease_expires_at > NEW.created_at
          AND attempt.lease_expires_at > CURRENT_TIMESTAMP
          AND node_lease.holder_kind = 'recovery_job'
          AND node_lease.owner_id = attempt.owner_id
          AND node_lease.fence = NEW.node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
          AND node_lease.lease_expires_at > CURRENT_TIMESTAMP
          AND NEW.sequence = COALESCE((
              SELECT MAX(existing.sequence) + 1
              FROM backup_asset_recovery_checkpoints AS existing
              WHERE existing.job_id = NEW.job_id
          ), 0)
          AND (
              (NEW.sequence = 0 AND job.target_mode = 'in_place')
              OR (NEW.sequence > 0 AND EXISTS (
                  SELECT 1
                  FROM backup_asset_recovery_checkpoints AS predecessor
                  WHERE predecessor.job_id = NEW.job_id
                    AND predecessor.sequence = NEW.sequence - 1
                    AND predecessor.phase IN (
                        'workspace_reserved', 'operation', 'delete_authority_consumed'
                    )
              ))
          )
    ) THEN
        RAISE EXCEPTION 'unresolved operation checkpoint binding mismatch';
    END IF;
    IF NEW.phase = 'delete_authority_required' AND NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_jobs AS job
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        JOIN backup_asset_recovery_attempts AS attempt
          ON attempt.id = NEW.attempt_id AND attempt.job_id = NEW.job_id
        JOIN backup_asset_recovery_node_leases AS node_lease
          ON node_lease.job_id = NEW.job_id AND node_lease.attempt_id = NEW.attempt_id
        WHERE job.id = NEW.job_id
          AND job.state = 'running'
          AND job.target_mode = 'in_place'
          AND job.delete_set_digest = NEW.operation_digest
          AND job.target_chain_revision = NEW.prior_target_revision
          AND plan.target_mode = 'in_place'
          AND plan.conflict_policy = 'exact_mirror'
          AND plan.root_revision = NEW.delete_root_revision
          AND attempt.fence = NEW.attempt_fence
          AND attempt.state IN ('claimed', 'running')
          AND attempt.lease_expires_at > NEW.created_at
          AND node_lease.fence = NEW.node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
    ) THEN
        RAISE EXCEPTION 'delete-authority checkpoint binding mismatch';
    END IF;
    IF NEW.phase = 'delete_authority_consumed' AND NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_grants AS delete_grant
        JOIN backup_asset_recovery_checkpoints AS required
          ON required.id = delete_grant.delete_checkpoint_id
        JOIN backup_asset_recovery_jobs AS job ON job.id = required.job_id
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        JOIN backup_asset_recovery_attempts AS attempt
          ON attempt.id = required.attempt_id AND attempt.job_id = required.job_id
        JOIN backup_asset_recovery_node_leases AS node_lease
          ON node_lease.job_id = required.job_id
         AND node_lease.attempt_id = required.attempt_id
        WHERE delete_grant.id = NEW.delete_grant_id
          AND delete_grant.authority_category = 'exact_mirror_delete'
          AND delete_grant.plan_id = job.plan_id
          AND delete_grant.job_id = NEW.job_id
          AND delete_grant.delete_checkpoint_id = required.id
          AND delete_grant.delete_set_digest = NEW.operation_digest
          AND delete_grant.delete_target_revision = NEW.prior_target_revision
          AND delete_grant.delete_attempt_id = NEW.attempt_id
          AND delete_grant.delete_attempt_fence = NEW.attempt_fence
          AND delete_grant.delete_node_fence = NEW.node_fence
          AND delete_grant.binding_digest = NEW.delete_grant_binding_digest
          AND delete_grant.expires_at = NEW.delete_grant_expires_at
          AND delete_grant.consumed_at = NEW.delete_grant_consumed_at
          AND delete_grant.consumed_at = NEW.created_at
          AND delete_grant.expires_at > NEW.created_at
          AND delete_grant.revoked_at IS NULL
          AND required.phase = 'delete_authority_required'
          AND required.sequence + 1 = NEW.sequence
          AND required.job_id = NEW.job_id
          AND required.attempt_id = NEW.attempt_id
          AND required.operation_digest = NEW.operation_digest
          AND required.prior_target_revision = NEW.prior_target_revision
          AND required.node_fence = NEW.node_fence
          AND required.attempt_fence = NEW.attempt_fence
          AND required.delete_node_revision = NEW.delete_node_revision
          AND required.delete_root_revision = NEW.delete_root_revision
          AND required.delete_authority_expires_at = NEW.delete_authority_expires_at
          AND required.delete_authority_expires_at >= delete_grant.expires_at
          AND job.state = 'running'
          AND job.target_mode = 'in_place'
          AND job.delete_set_digest = NEW.operation_digest
          AND job.target_chain_revision = NEW.prior_target_revision
          AND plan.target_mode = 'in_place'
          AND plan.conflict_policy = 'exact_mirror'
          AND plan.root_revision = NEW.delete_root_revision
          AND attempt.fence = NEW.attempt_fence
          AND attempt.state IN ('claimed', 'running')
          AND attempt.lease_expires_at > NEW.created_at
          AND node_lease.fence = NEW.node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
    ) THEN
        RAISE EXCEPTION 'consumed exact-mirror delete authority binding mismatch';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_checkpoints_authority_insert
BEFORE INSERT ON backup_asset_recovery_checkpoints
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_checkpoint_authority_insert_guard();

-- Failure rows are enforced by trg_backup_asset_recovery_evidence_receipt_insert.
-- Keep the down-with-table snapshot name without adding a function dependency
-- that the frozen down migration would have to remove first.
CREATE TRIGGER trg_backup_asset_recovery_evidence_worker_insert
BEFORE INSERT ON backup_asset_recovery_evidence
FOR EACH ROW WHEN (false)
EXECUTE FUNCTION pg_catalog.suppress_redundant_updates_trigger();

CREATE TRIGGER trg_backup_asset_recovery_checkpoints_immutable
BEFORE UPDATE ON backup_asset_recovery_checkpoints
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_frozen_product_guard();

CREATE TRIGGER trg_backup_asset_recovery_checkpoints_consumed_delete
BEFORE DELETE ON backup_asset_recovery_checkpoints
FOR EACH ROW WHEN (OLD.phase = 'delete_authority_consumed')
EXECUTE FUNCTION backup_asset_recovery_frozen_product_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_checkpoint_consumed_replay_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM backup_asset_recovery_checkpoints
        WHERE id = NEW.id AND phase = 'delete_authority_consumed'
    ) THEN
        RAISE EXCEPTION 'consumed recovery checkpoint cannot be replaced';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_checkpoints_consumed_replay
BEFORE INSERT ON backup_asset_recovery_checkpoints
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_checkpoint_consumed_replay_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_job_publication_integrity_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.workspace_marker_binding_digest <> ''
       AND NEW.workspace_marker_binding_digest IS DISTINCT FROM OLD.workspace_marker_binding_digest THEN
        RAISE EXCEPTION 'recovery workspace marker binding is immutable';
    END IF;
	IF OLD.workspace_owner <> '' AND NEW.workspace_owner IS DISTINCT FROM OLD.workspace_owner THEN
		RAISE EXCEPTION 'recovery workspace owner provenance is immutable';
	END IF;
	IF OLD.workspace_fence <> 0 AND NEW.workspace_fence IS DISTINCT FROM OLD.workspace_fence THEN
		RAISE EXCEPTION 'recovery workspace fence provenance is immutable';
	END IF;
    IF (NEW.workspace_marker_validation_attempt_id IS DISTINCT FROM OLD.workspace_marker_validation_attempt_id
            OR NEW.workspace_marker_validation_attempt_fence IS DISTINCT FROM OLD.workspace_marker_validation_attempt_fence
            OR NEW.workspace_marker_validation_node_fence IS DISTINCT FROM OLD.workspace_marker_validation_node_fence)
       AND NOT (
            OLD.target_mode = 'isolated' AND NEW.target_mode = 'isolated'
            AND OLD.workspace_phase = 'reserved' AND NEW.workspace_phase = 'marker_created'
            AND OLD.workspace_marker_validation_attempt_id = ''
            AND OLD.workspace_marker_validation_attempt_fence = 0
            AND OLD.workspace_marker_validation_node_fence = 0
            AND NEW.workspace_marker_validation_attempt_id ~ '^[0-9a-f]{32}$'
            AND NEW.workspace_marker_validation_attempt_fence > 0
            AND NEW.workspace_marker_validation_node_fence > 0
            AND EXISTS (
                SELECT 1
                FROM backup_asset_recovery_attempts AS attempt
                JOIN backup_asset_recovery_node_leases AS node_lease
                  ON node_lease.job_id = OLD.id
                 AND node_lease.attempt_id = attempt.id
                 AND node_lease.owner_id = attempt.owner_id
                 AND node_lease.fence = NEW.workspace_marker_validation_node_fence
                 AND node_lease.state = 'active'
                WHERE attempt.id = NEW.workspace_marker_validation_attempt_id
                  AND attempt.job_id = OLD.id
                  AND attempt.fence = NEW.workspace_marker_validation_attempt_fence
                  AND attempt.state = 'running'
                  AND attempt.mutation_armed
            )
       ) THEN
        RAISE EXCEPTION 'recovery workspace marker validation provenance is invalid';
    END IF;
    IF OLD.plaintext_deadline IS NOT NULL
       AND NEW.plaintext_deadline IS DISTINCT FROM OLD.plaintext_deadline THEN
        RAISE EXCEPTION 'recovery workspace plaintext deadline is immutable';
    END IF;
    IF NEW.workspace_phase IS DISTINCT FROM OLD.workspace_phase AND NOT (
        OLD.target_mode = 'isolated' AND NEW.target_mode = 'isolated' AND (
            (OLD.workspace_phase = 'none' AND NEW.workspace_phase = 'reserved')
            OR (OLD.workspace_phase = 'reserved' AND NEW.workspace_phase IN ('marker_created', 'cleanup_due'))
            OR (OLD.workspace_phase = 'marker_created' AND NEW.workspace_phase IN ('writing', 'cleanup_due'))
            OR (OLD.workspace_phase = 'writing' AND NEW.workspace_phase IN ('sealed', 'cleanup_due'))
            OR (OLD.workspace_phase = 'sealed' AND NEW.workspace_phase IN ('published', 'cleanup_due'))
            OR (OLD.workspace_phase = 'cleanup_due' AND NEW.workspace_phase = 'workspace_cleaned')
        )
    ) THEN
        RAISE EXCEPTION 'recovery workspace phase transition is invalid';
    END IF;
    IF NEW.workspace_phase = 'published' AND EXISTS (
        SELECT 1 FROM backup_asset_recovery_attempts
        WHERE job_id = OLD.id AND state IN ('claimed', 'running')
    ) THEN
        RAISE EXCEPTION 'recovery job cannot publish with an active attempt';
    END IF;
    IF EXISTS (SELECT 1 FROM backup_asset_recovery_result_sets WHERE job_id = OLD.id)
       AND (
            NEW.state IS DISTINCT FROM OLD.state
            OR NEW.failure_category IS DISTINCT FROM OLD.failure_category
            OR NEW.transition_revision IS DISTINCT FROM OLD.transition_revision
            OR NEW.workspace_phase IS DISTINCT FROM OLD.workspace_phase
            OR NEW.encrypted_workspace_relative_locator IS DISTINCT FROM OLD.encrypted_workspace_relative_locator
            OR NEW.workspace_marker_binding_digest IS DISTINCT FROM OLD.workspace_marker_binding_digest
            OR NEW.workspace_owner IS DISTINCT FROM OLD.workspace_owner
            OR NEW.workspace_fence IS DISTINCT FROM OLD.workspace_fence
            OR NEW.workspace_marker_validation_attempt_id IS DISTINCT FROM OLD.workspace_marker_validation_attempt_id
            OR NEW.workspace_marker_validation_attempt_fence IS DISTINCT FROM OLD.workspace_marker_validation_attempt_fence
            OR NEW.workspace_marker_validation_node_fence IS DISTINCT FROM OLD.workspace_marker_validation_node_fence
            OR NEW.workspace_cleanup_phase IS DISTINCT FROM OLD.workspace_cleanup_phase
            OR NEW.workspace_cleanup_owner IS DISTINCT FROM OLD.workspace_cleanup_owner
            OR NEW.workspace_cleanup_lease_expires_at IS DISTINCT FROM OLD.workspace_cleanup_lease_expires_at
            OR NEW.workspace_cleanup_fence IS DISTINCT FROM OLD.workspace_cleanup_fence
            OR NEW.workspace_cleanup_node_lease_id IS DISTINCT FROM OLD.workspace_cleanup_node_lease_id
            OR NEW.workspace_cleanup_node_fence IS DISTINCT FROM OLD.workspace_cleanup_node_fence
            OR NEW.workspace_cleanup_attempt IS DISTINCT FROM OLD.workspace_cleanup_attempt
            OR NEW.plaintext_deadline IS DISTINCT FROM OLD.plaintext_deadline
            OR NEW.target_chain_revision IS DISTINCT FROM OLD.target_chain_revision
       ) THEN
        RAISE EXCEPTION 'published recovery result requires a terminal published job';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_jobs_publication_integrity
BEFORE UPDATE OF state, failure_category, transition_revision, workspace_phase,
    encrypted_workspace_relative_locator, workspace_marker_binding_digest,
    workspace_owner, workspace_fence, workspace_marker_validation_attempt_id,
    workspace_marker_validation_attempt_fence, workspace_marker_validation_node_fence,
    workspace_cleanup_phase, workspace_cleanup_owner, workspace_cleanup_lease_expires_at,
    workspace_cleanup_fence, workspace_cleanup_node_lease_id,
    workspace_cleanup_node_fence, workspace_cleanup_attempt,
    plaintext_deadline, target_chain_revision
ON backup_asset_recovery_jobs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_publication_integrity_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_job_state_transition_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.state IS DISTINCT FROM OLD.state AND NOT (
            (OLD.state = 'queued' AND NEW.state IN ('running', 'cancel_requested'))
            OR (OLD.state = 'running' AND NEW.state IN ('verifying', 'cancel_requested', 'failed', 'needs_attention'))
            OR (OLD.state = 'verifying' AND NEW.state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'cancel_requested'))
            OR (OLD.state = 'cancel_requested' AND NEW.state IN ('canceled', 'needs_attention'))
        ))
        OR (NEW.state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'canceled') AND EXISTS (
            SELECT 1 FROM backup_asset_recovery_attempts
            WHERE job_id = OLD.id AND state IN ('claimed', 'running')
        )) THEN
        RAISE EXCEPTION 'recovery job state transition is invalid or has an active attempt';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_jobs_state_transition
BEFORE UPDATE OF state ON backup_asset_recovery_jobs
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_job_state_transition_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_attempt_publication_integrity_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state IN ('claimed', 'running') AND EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs AS job
        WHERE job.id = NEW.job_id
          AND (job.workspace_phase = 'published' OR EXISTS (
            SELECT 1 FROM backup_asset_recovery_result_sets WHERE job_id = job.id
          ))
    ) THEN
        RAISE EXCEPTION 'published recovery job cannot acquire an active attempt';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_attempts_publication_integrity
BEFORE INSERT ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_publication_integrity_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_attempt_terminal_job_barrier_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state IN ('claimed', 'running') AND EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs
        WHERE id = NEW.job_id AND state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'canceled')
    ) THEN
        RAISE EXCEPTION 'terminal recovery job cannot acquire an active attempt';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_attempts_terminal_job_barrier
BEFORE INSERT ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_terminal_job_barrier_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_result_set_publish_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state <> 'ready' OR NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs
        WHERE id = NEW.job_id AND target_mode = 'isolated' AND workspace_phase = 'published'
          AND state IN ('succeeded', 'degraded')
          AND workspace_marker_binding_digest = NEW.marker_binding_digest
          AND plaintext_deadline = NEW.plaintext_deadline
          AND NOT EXISTS (
            SELECT 1 FROM backup_asset_recovery_attempts
            WHERE job_id = NEW.job_id AND state IN ('claimed', 'running')
          )
    ) THEN
        RAISE EXCEPTION 'recovery result set requires published isolated terminal job';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_publish
BEFORE INSERT ON backup_asset_recovery_result_sets
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_result_set_publish_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_result_set_deadline_integrity_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.marker_binding_digest IS DISTINCT FROM OLD.marker_binding_digest
       OR NEW.hard_deadline IS DISTINCT FROM OLD.hard_deadline
       OR (OLD.state = 'cleaned' AND NEW.plaintext_deadline IS DISTINCT FROM OLD.plaintext_deadline)
       OR NEW.plaintext_deadline < OLD.plaintext_deadline
       OR NEW.plaintext_deadline > OLD.hard_deadline THEN
        RAISE EXCEPTION 'recovery result publication marker and deadlines are immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_deadline_integrity
BEFORE UPDATE OF marker_binding_digest, plaintext_deadline, hard_deadline
ON backup_asset_recovery_result_sets
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_result_set_deadline_integrity_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_result_set_state_transition_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.state IS DISTINCT FROM OLD.state AND NOT (
            (OLD.state = 'ready' AND NEW.state = 'revoking'
                AND NEW.cleanup_fence > OLD.cleanup_fence
                AND NEW.cleanup_attempt > OLD.cleanup_attempt)
            OR (OLD.state = 'revoking' AND NEW.state IN ('cleaned', 'cleanup_failed')
                AND NEW.cleanup_fence = OLD.cleanup_fence
                AND NEW.cleanup_attempt = OLD.cleanup_attempt)
            OR (OLD.state = 'cleanup_failed' AND NEW.state = 'revoking'
                AND NEW.cleanup_fence > OLD.cleanup_fence
                AND NEW.cleanup_attempt > OLD.cleanup_attempt)
        ))
        OR (OLD.state = 'revoking' AND NEW.state = 'revoking'
            AND (NEW.cleanup_owner IS DISTINCT FROM OLD.cleanup_owner
                OR NEW.cleanup_fence IS DISTINCT FROM OLD.cleanup_fence
                OR NEW.cleanup_attempt IS DISTINCT FROM OLD.cleanup_attempt
                OR NEW.node_lease_id IS DISTINCT FROM OLD.node_lease_id
                OR NEW.node_fence IS DISTINCT FROM OLD.node_fence)
            AND NOT (OLD.cleanup_lease_expires_at <= CURRENT_TIMESTAMP
                AND NEW.cleanup_fence > OLD.cleanup_fence
                AND NEW.cleanup_attempt > OLD.cleanup_attempt)) THEN
        RAISE EXCEPTION 'recovery result set state transition requires the current or a fresh expired fence';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_state_transition
BEFORE UPDATE ON backup_asset_recovery_result_sets
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_result_set_state_transition_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_result_set_terminal_delete_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state = 'cleaned' THEN
        RAISE EXCEPTION 'cleaned recovery result-set tombstone cannot be deleted';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_terminal_delete
BEFORE DELETE ON backup_asset_recovery_result_sets
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_result_set_terminal_delete_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_result_publish_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_result_sets
        WHERE id = NEW.result_set_id AND job_id = NEW.job_id AND state = 'ready'
    ) THEN
        RAISE EXCEPTION 'recovery result requires ready matching result set';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_results_publish
BEFORE INSERT ON backup_asset_recovery_results
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_result_publish_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_result_classification_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.classification IS DISTINCT FROM OLD.classification
       OR NEW.classification_revision IS DISTINCT FROM OLD.classification_revision
       OR NEW.classification_source_revision IS DISTINCT FROM OLD.classification_source_revision THEN
        RAISE EXCEPTION 'recovery result classification binding is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_results_classification_immutable
BEFORE UPDATE OF classification, classification_revision, classification_source_revision
ON backup_asset_recovery_results
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_result_classification_guard();

ALTER TABLE backup_asset_delivery_grants
    DROP CONSTRAINT backup_asset_delivery_grants_resource_check,
    DROP CONSTRAINT backup_asset_delivery_grants_security_product_check;
ALTER TABLE backup_asset_delivery_grants
    ADD CONSTRAINT backup_asset_delivery_grants_resource_check CHECK (
        (resource_kind = 'backup_asset' AND recovery_point_id IS NOT NULL AND catalog_generation_id IS NOT NULL AND entry_id IS NOT NULL
            AND recovery_job_id IS NULL AND recovery_result_id IS NULL)
        OR (resource_kind = 'recovery_result' AND recovery_point_id IS NULL AND catalog_generation_id IS NULL AND entry_id IS NULL
            AND recovery_job_id IS NOT NULL AND recovery_job_id ~ '^[0-9a-f]{32}$'
            AND recovery_result_id IS NOT NULL AND recovery_result_id ~ '^[0-9a-f]{32}$')
    ),
    ADD CONSTRAINT backup_asset_delivery_grants_security_product_check CHECK (
        (resource_kind = 'backup_asset' AND action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.download' AND step_up_proof_id IS NOT NULL
            AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (resource_kind = 'backup_asset' AND action = 'preview' AND renderer <> 'attachment' AND classification = 'non_secret'
            AND step_up_action IS NULL AND step_up_proof_id IS NULL AND step_up_expires_at IS NULL)
        OR (resource_kind = 'backup_asset' AND action = 'preview' AND renderer <> 'attachment' AND classification IN ('secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.secret_reveal' AND step_up_proof_id IS NOT NULL
            AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (resource_kind = 'recovery_result' AND action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND classification IN ('non_secret', 'secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'recovery.result_download'
            AND step_up_proof_id IS NOT NULL AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL
            AND absolute_expires_at <= step_up_expires_at)
    ),
    ADD CONSTRAINT backup_asset_delivery_grants_recovery_job_fk
        FOREIGN KEY (recovery_job_id) REFERENCES backup_asset_recovery_jobs(id) ON DELETE RESTRICT,
    ADD CONSTRAINT backup_asset_delivery_grants_recovery_result_fk
        FOREIGN KEY (recovery_result_id, recovery_job_id)
        REFERENCES backup_asset_recovery_results(id, job_id) ON DELETE RESTRICT;
CREATE INDEX idx_backup_asset_delivery_grants_recovery_result_state
    ON backup_asset_delivery_grants(recovery_job_id, recovery_result_id, state);

CREATE OR REPLACE FUNCTION backup_asset_recovery_content_authorization_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.session_role <> 'admin'
       OR NEW.action <> 'download'
       OR NEW.renderer <> 'attachment'
       OR NEW.profile <> 'original_v1'
       OR NEW.step_up_action IS DISTINCT FROM 'recovery.result_download'
       OR NEW.step_up_proof_id IS NULL
       OR NOT EXISTS (
            SELECT 1
            FROM backup_asset_recovery_results AS result
            JOIN backup_asset_recovery_result_sets AS result_set
              ON result_set.id = result.result_set_id
             AND result_set.job_id = result.job_id
            JOIN backup_asset_recovery_jobs AS job
              ON job.id = result.job_id
            JOIN backup_asset_recovery_plans AS plan
              ON plan.id = job.plan_id
            JOIN backup_asset_recovery_grants AS authority
              ON authority.id = job.authority_grant_id
             AND authority.plan_id = job.plan_id
            WHERE result.id = NEW.recovery_result_id
              AND result.job_id = NEW.recovery_job_id
              AND result.classification = NEW.classification
              AND result.classification_revision = NEW.classification_revision
              AND result.classification_source_revision = NEW.classification_source_revision
              AND result_set.state = 'ready'
              AND job.target_mode = 'isolated'
              AND job.state IN ('succeeded', 'degraded')
              AND job.workspace_phase = 'published'
              AND job.security_decision IN ('allow_clean', 'admin_override')
              AND job.authority_category = 'write'
              AND authority.authority_category = 'write'
              AND authority.binding_digest = job.authority_binding_digest
              AND authority.expires_at = job.authority_expires_at
              AND authority.consumed_at = job.authority_consumed_at
              AND authority.consumed_at IS NOT NULL
              AND authority.revoked_at IS NULL
              AND plan.requester_id = NEW.owner_user_id
              AND NOT EXISTS (
                  SELECT 1 FROM backup_asset_recovery_attempts
                  WHERE job_id = job.id AND state IN ('claimed', 'running')
              )
       ) THEN
        RAISE EXCEPTION 'recovery result Content authorization binding mismatch';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_content_authorization_insert
BEFORE INSERT ON backup_asset_delivery_grants
FOR EACH ROW WHEN (NEW.resource_kind = 'recovery_result')
EXECUTE FUNCTION backup_asset_recovery_content_authorization_guard();
CREATE TRIGGER trg_backup_asset_recovery_content_authorization_update
BEFORE UPDATE OF resource_kind ON backup_asset_delivery_grants
FOR EACH ROW WHEN (OLD.resource_kind <> 'recovery_result' AND NEW.resource_kind = 'recovery_result')
EXECUTE FUNCTION backup_asset_recovery_content_authorization_guard();

CREATE OR REPLACE FUNCTION backup_asset_recovery_content_binding_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.resource_kind = 'recovery_result' AND (
        NEW.id IS DISTINCT FROM OLD.id
        OR NEW.delivery_id IS DISTINCT FROM OLD.delivery_id
        OR NEW.resource_kind IS DISTINCT FROM OLD.resource_kind
        OR NEW.recovery_point_id IS DISTINCT FROM OLD.recovery_point_id
        OR NEW.catalog_generation_id IS DISTINCT FROM OLD.catalog_generation_id
        OR NEW.entry_id IS DISTINCT FROM OLD.entry_id
        OR NEW.recovery_job_id IS DISTINCT FROM OLD.recovery_job_id
        OR NEW.recovery_result_id IS DISTINCT FROM OLD.recovery_result_id
        OR NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id
        OR NEW.session_jti IS DISTINCT FROM OLD.session_jti
        OR NEW.session_token_version IS DISTINCT FROM OLD.session_token_version
        OR NEW.session_role IS DISTINCT FROM OLD.session_role
        OR NEW.session_expires_at IS DISTINCT FROM OLD.session_expires_at
        OR NEW.action IS DISTINCT FROM OLD.action
        OR NEW.method_policy IS DISTINCT FROM OLD.method_policy
        OR NEW.renderer IS DISTINCT FROM OLD.renderer
        OR NEW.profile IS DISTINCT FROM OLD.profile
        OR NEW.classification IS DISTINCT FROM OLD.classification
        OR NEW.classification_revision IS DISTINCT FROM OLD.classification_revision
        OR NEW.classification_source_revision IS DISTINCT FROM OLD.classification_source_revision
        OR NEW.step_up_action IS DISTINCT FROM OLD.step_up_action
        OR NEW.step_up_proof_id IS DISTINCT FROM OLD.step_up_proof_id
        OR NEW.step_up_expires_at IS DISTINCT FROM OLD.step_up_expires_at
        OR NEW.absolute_expires_at IS DISTINCT FROM OLD.absolute_expires_at
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
    ) THEN
        RAISE EXCEPTION 'recovery result Content authorization binding is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_content_binding_immutable
BEFORE UPDATE OF
    id, delivery_id, resource_kind, recovery_point_id, catalog_generation_id, entry_id,
    recovery_job_id, recovery_result_id, owner_user_id, session_jti,
    session_token_version, session_role, session_expires_at, action, method_policy,
    renderer, profile, classification, classification_revision,
    classification_source_revision, step_up_action, step_up_proof_id,
    step_up_expires_at, absolute_expires_at, created_at
ON backup_asset_delivery_grants
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_content_binding_guard();

-- golang-migrate marks the target version dirty before it executes a down
-- body. Reject a used 000069 downgrade at that durable metadata boundary so
-- its SetVersion transaction rolls back and leaves version 69 clean.
CREATE OR REPLACE FUNCTION backup_asset_recovery_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 69 AND (
        EXISTS (SELECT 1 FROM backup_asset_recovery_plans)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_plan_items)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_preflights)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_grants)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_jobs)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_job_items)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_attempts)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_checkpoints)
        OR EXISTS (
            SELECT 1 FROM backup_asset_recovery_evidence
            WHERE NOT (
                kind = 'scheduler_state'
                AND ((id = '0000000000000000000000000000006a' AND scheduler_scope = 'claim')
                    OR (id = '0000000000000000000000000000006b' AND scheduler_scope = 'takeover'))
            )
        )
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_result_sets)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_results)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_node_leases)
        OR EXISTS (SELECT 1 FROM backup_asset_delivery_grants WHERE resource_kind = 'recovery_result')
        OR EXISTS (
            SELECT 1
            FROM backup_asset_delivery_requests AS request_row
            JOIN backup_asset_delivery_grants AS grant_row ON grant_row.id = request_row.grant_id
            WHERE grant_row.resource_kind = 'recovery_result'
        )
        OR EXISTS (SELECT 1 FROM backup_asset_delivery_usage)
        OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'content_session')
        OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'recovery_job' AND status = 'active')
    ) THEN
        RAISE EXCEPTION '000069 downgrade blocked: recovery, recovery content, or recovery lease state exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_recovery_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_downgrade_admission();

COMMIT;
