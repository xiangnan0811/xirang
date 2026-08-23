ALTER TABLE task_runs ADD COLUMN node_id_snapshot INTEGER;
UPDATE task_runs
SET node_id_snapshot = CASE
    WHEN EXISTS (SELECT 1 FROM tasks WHERE tasks.id = task_runs.task_id)
        THEN (SELECT tasks.node_id FROM tasks WHERE tasks.id = task_runs.task_id)
    WHEN status IN ('success', 'failed', 'canceled', 'warning', 'skipped')
        THEN 0
    ELSE NULL
END;
-- SQLite cannot tighten an added column to NOT NULL without rebuilding the
-- shared TaskRun table. Validate every legacy row inside the migration
-- transaction before installing any 000069 object; a failure rolls the ALTER
-- and backfill back and leaves golang-migrate dirty so startup fails closed.
CREATE TEMP TABLE backup_asset_069_task_run_snapshot_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO backup_asset_069_task_run_snapshot_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM task_runs AS run
    WHERE run.node_id_snapshot IS NULL
       OR run.node_id_snapshot < 0
       OR (
            run.node_id_snapshot = 0
            AND (
                run.status NOT IN ('success', 'failed', 'canceled', 'warning', 'skipped')
                OR EXISTS (SELECT 1 FROM tasks AS task_row WHERE task_row.id = run.task_id)
            )
       )
       OR (
            run.node_id_snapshot > 0
            AND NOT EXISTS (
                SELECT 1 FROM tasks AS task_row
                WHERE task_row.id = run.task_id
                  AND task_row.node_id = run.node_id_snapshot
                  AND task_row.node_id > 0
            )
       )
) THEN 0 ELSE 1 END;
DROP TABLE backup_asset_069_task_run_snapshot_guard;
CREATE INDEX idx_task_runs_node_snapshot_status
    ON task_runs(node_id_snapshot, status);

CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_insert
BEFORE INSERT ON task_runs
WHEN NEW.node_id_snapshot IS NULL
    OR NEW.node_id_snapshot <= 0
    OR NOT EXISTS (
        SELECT 1 FROM tasks
        WHERE tasks.id = NEW.task_id
          AND tasks.node_id = NEW.node_id_snapshot
    )
BEGIN
    SELECT RAISE(ABORT, 'TaskRun node snapshot must match the Task node at creation');
END;

CREATE TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_immutable
BEFORE UPDATE OF task_id, node_id_snapshot ON task_runs
WHEN NEW.task_id IS NOT OLD.task_id
    OR NEW.node_id_snapshot IS NOT OLD.node_id_snapshot
BEGIN
    SELECT RAISE(ABORT, 'TaskRun task and node snapshot are immutable');
END;

CREATE UNIQUE INDEX idx_recovery_points_repository_id_id
    ON recovery_points(repository_id, id);

CREATE TABLE backup_asset_recovery_plans (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    requester_id INTEGER NOT NULL,
    endpoint TEXT NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 64),
    idempotency_key_digest TEXT NOT NULL CHECK (length(idempotency_key_digest) = 64 AND idempotency_key_digest NOT GLOB '*[^0-9a-f]*'),
    repository_id TEXT NOT NULL CHECK (length(repository_id) = 32 AND repository_id NOT GLOB '*[^0-9a-f]*'),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32 AND recovery_point_id NOT GLOB '*[^0-9a-f]*'),
    source_revision_digest TEXT NOT NULL CHECK (length(source_revision_digest) = 64 AND source_revision_digest NOT GLOB '*[^0-9a-f]*'),
    source_revision_kind TEXT NOT NULL CHECK (source_revision_kind IN ('immutable', 'observation')),
    immutable_locator_digest TEXT NOT NULL DEFAULT '' CHECK (immutable_locator_digest = ''
        OR (length(immutable_locator_digest) = 64 AND immutable_locator_digest NOT GLOB '*[^0-9a-f]*')),
    immutable_manifest_digest TEXT NOT NULL DEFAULT '' CHECK (immutable_manifest_digest = ''
        OR (length(immutable_manifest_digest) = 64 AND immutable_manifest_digest NOT GLOB '*[^0-9a-f]*')),
    observation_fingerprint TEXT NOT NULL DEFAULT '' CHECK (observation_fingerprint = ''
        OR (length(observation_fingerprint) = 64 AND observation_fingerprint NOT GLOB '*[^0-9a-f]*')),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32 AND catalog_generation_id NOT GLOB '*[^0-9a-f]*'),
    observed_at DATETIME,
    encrypted_source_locator TEXT NOT NULL DEFAULT '',
    target_mode TEXT NOT NULL CHECK (target_mode IN ('isolated', 'in_place')),
    target_node_id INTEGER NOT NULL,
    target_root_id TEXT NOT NULL CHECK (length(target_root_id) BETWEEN 1 AND 32),
    encrypted_target_root_locator TEXT NOT NULL DEFAULT '',
    encrypted_target_relative_path TEXT NOT NULL DEFAULT '',
    root_locator_digest TEXT NOT NULL CHECK (length(root_locator_digest) = 64 AND root_locator_digest NOT GLOB '*[^0-9a-f]*'),
    path_digest TEXT NOT NULL CHECK (length(path_digest) = 64 AND path_digest NOT GLOB '*[^0-9a-f]*'),
    target_base_revision TEXT NOT NULL CHECK (length(target_base_revision) BETWEEN 1 AND 64),
    credential_scope_revision TEXT NOT NULL CHECK (length(credential_scope_revision) BETWEEN 1 AND 64),
    root_revision TEXT NOT NULL CHECK (length(root_revision) BETWEEN 1 AND 64),
    filesystem_revision TEXT NOT NULL CHECK (length(filesystem_revision) BETWEEN 1 AND 64),
    selection_digest TEXT NOT NULL CHECK (length(selection_digest) = 64 AND selection_digest NOT GLOB '*[^0-9a-f]*'),
    binding_digest TEXT NOT NULL CHECK (length(binding_digest) = 64 AND binding_digest NOT GLOB '*[^0-9a-f]*'),
    capability_revision TEXT NOT NULL CHECK (length(capability_revision) BETWEEN 1 AND 64),
    conflict_policy TEXT NOT NULL CHECK (conflict_policy IN ('fail_on_conflict', 'skip_existing', 'overwrite_selected', 'exact_mirror')),
    operation_set_digest TEXT NOT NULL CHECK (length(operation_set_digest) = 64 AND operation_set_digest NOT GLOB '*[^0-9a-f]*'),
    delete_set_digest TEXT NOT NULL CHECK (length(delete_set_digest) = 64 AND delete_set_digest NOT GLOB '*[^0-9a-f]*'),
    security_decision TEXT NOT NULL CHECK (security_decision IN ('allow_clean', 'block', 'admin_override')),
    security_decision_digest TEXT NOT NULL CHECK (length(security_decision_digest) = 64 AND security_decision_digest NOT GLOB '*[^0-9a-f]*'),
    security_finding_set_digest TEXT NOT NULL CHECK (length(security_finding_set_digest) = 64 AND security_finding_set_digest NOT GLOB '*[^0-9a-f]*'),
    security_policy_revision TEXT NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 64),
    security_override_binding_digest TEXT NOT NULL DEFAULT '' CHECK (security_override_binding_digest = ''
        OR (length(security_override_binding_digest) = 64 AND security_override_binding_digest NOT GLOB '*[^0-9a-f]*')),
    encrypted_override_reason TEXT NOT NULL DEFAULT '',
    preflight_revision TEXT NOT NULL CHECK (length(preflight_revision) BETWEEN 1 AND 64),
    preflight_expires_at DATETIME NOT NULL,
    estimated_items INTEGER NOT NULL DEFAULT 0 CHECK (estimated_items >= 0),
    estimated_bytes INTEGER NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    state TEXT NOT NULL CHECK (state IN ('draft', 'preflight_ready', 'authorized', 'executed', 'canceled', 'superseded', 'expired')),
    transition_revision INTEGER NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (requester_id, endpoint, idempotency_key_digest),
    CHECK (
        (source_revision_kind = 'immutable'
            AND length(immutable_locator_digest) = 64 AND length(immutable_manifest_digest) = 64
            AND observation_fingerprint = '' AND observed_at IS NULL)
        OR
        (source_revision_kind = 'observation'
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
    FOREIGN KEY (requester_id) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (repository_id) REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE RESTRICT,
    FOREIGN KEY (repository_id, recovery_point_id)
        REFERENCES recovery_points(repository_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (target_node_id) REFERENCES nodes(id) ON DELETE RESTRICT
);

-- SQLite cannot name an expression-free duplicate primary-key tuple inline;
-- these composite uniqueness indexes are the cross-table parity anchors.
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
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plan_id TEXT NOT NULL CHECK (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*'),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32 AND recovery_point_id NOT GLOB '*[^0-9a-f]*'),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32 AND catalog_generation_id NOT GLOB '*[^0-9a-f]*'),
    entry_id TEXT NOT NULL CHECK (length(entry_id) = 64 AND entry_id NOT GLOB '*[^0-9a-f]*'),
    entry_type TEXT NOT NULL CHECK (entry_type IN ('file', 'directory', 'symlink', 'hardlink', 'special', 'unknown')),
    source_fingerprint TEXT NOT NULL DEFAULT '' CHECK (source_fingerprint = ''
        OR (length(source_fingerprint) = 64 AND source_fingerprint NOT GLOB '*[^0-9a-f]*')),
    relative_path_digest TEXT NOT NULL CHECK (length(relative_path_digest) = 64 AND relative_path_digest NOT GLOB '*[^0-9a-f]*'),
    created_at DATETIME NOT NULL,
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
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plan_id TEXT NOT NULL CHECK (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*'),
    revision TEXT NOT NULL CHECK (length(revision) BETWEEN 1 AND 64),
    source_revision_digest TEXT NOT NULL CHECK (length(source_revision_digest) = 64 AND source_revision_digest NOT GLOB '*[^0-9a-f]*'),
    target_node_id INTEGER NOT NULL,
    node_revision TEXT NOT NULL CHECK (length(node_revision) BETWEEN 1 AND 64),
    target_root_id TEXT NOT NULL CHECK (length(target_root_id) BETWEEN 1 AND 32),
    root_locator_digest TEXT NOT NULL CHECK (length(root_locator_digest) = 64 AND root_locator_digest NOT GLOB '*[^0-9a-f]*'),
    path_digest TEXT NOT NULL CHECK (length(path_digest) = 64 AND path_digest NOT GLOB '*[^0-9a-f]*'),
    target_revision TEXT NOT NULL CHECK (length(target_revision) BETWEEN 1 AND 64),
    capability_revision TEXT NOT NULL CHECK (length(capability_revision) BETWEEN 1 AND 64),
    policy_revision TEXT NOT NULL CHECK (length(policy_revision) BETWEEN 1 AND 64),
    finding_set_digest TEXT NOT NULL CHECK (length(finding_set_digest) = 64 AND finding_set_digest NOT GLOB '*[^0-9a-f]*'),
    security_override_candidate_digest TEXT NOT NULL DEFAULT '' CHECK (security_override_candidate_digest = ''
        OR (length(security_override_candidate_digest) = 64 AND security_override_candidate_digest NOT GLOB '*[^0-9a-f]*')),
    security_override_categories TEXT NOT NULL DEFAULT '' CHECK (security_override_categories IN (
        '', 'malware', 'suspicious', 'test_signature', 'malware,suspicious',
        'malware,test_signature', 'suspicious,test_signature', 'malware,suspicious,test_signature'
    )),
    operation_set_digest TEXT NOT NULL CHECK (length(operation_set_digest) = 64 AND operation_set_digest NOT GLOB '*[^0-9a-f]*'),
    delete_set_digest TEXT NOT NULL CHECK (length(delete_set_digest) = 64 AND delete_set_digest NOT GLOB '*[^0-9a-f]*'),
    encrypted_operation_rows TEXT NOT NULL CHECK (length(encrypted_operation_rows) > 0),
    estimated_items INTEGER NOT NULL DEFAULT 0 CHECK (estimated_items >= 0),
    estimated_bytes INTEGER NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    UNIQUE (id, plan_id),
    UNIQUE (id, plan_id, target_node_id, target_root_id, root_locator_digest, path_digest, node_revision),
    UNIQUE (plan_id, revision),
    CHECK (expires_at > created_at),
    CHECK (
        (security_override_candidate_digest = '' AND security_override_categories = '')
        OR (length(security_override_candidate_digest) = 64 AND security_override_categories <> '')
    ),
    FOREIGN KEY (plan_id) REFERENCES backup_asset_recovery_plans(id) ON DELETE CASCADE,
    FOREIGN KEY (plan_id, target_node_id, target_root_id, root_locator_digest, path_digest, node_revision)
        REFERENCES backup_asset_recovery_plans(
            id, target_node_id, target_root_id, root_locator_digest, path_digest, target_base_revision
        ) ON DELETE CASCADE,
    FOREIGN KEY (target_node_id) REFERENCES nodes(id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_recovery_preflights_plan_expiry
    ON backup_asset_recovery_preflights(plan_id, expires_at DESC);

CREATE TABLE backup_asset_recovery_jobs (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plan_id TEXT NOT NULL CHECK (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*'),
    plan_binding_digest TEXT NOT NULL CHECK (length(plan_binding_digest) = 64 AND plan_binding_digest NOT GLOB '*[^0-9a-f]*'),
    selection_digest TEXT NOT NULL CHECK (length(selection_digest) = 64 AND selection_digest NOT GLOB '*[^0-9a-f]*'),
    source_revision_digest TEXT NOT NULL CHECK (length(source_revision_digest) = 64 AND source_revision_digest NOT GLOB '*[^0-9a-f]*'),
    preflight_id TEXT NOT NULL CHECK (length(preflight_id) = 32 AND preflight_id NOT GLOB '*[^0-9a-f]*'),
    preflight_revision TEXT NOT NULL CHECK (length(preflight_revision) BETWEEN 1 AND 64),
    preflight_expires_at DATETIME NOT NULL,
    preflight_target_revision TEXT NOT NULL CHECK (length(preflight_target_revision) BETWEEN 1 AND 64),
    preflight_node_revision TEXT NOT NULL CHECK (length(preflight_node_revision) BETWEEN 1 AND 64),
    capability_revision TEXT NOT NULL CHECK (length(capability_revision) BETWEEN 1 AND 64),
    operation_set_digest TEXT NOT NULL CHECK (length(operation_set_digest) = 64 AND operation_set_digest NOT GLOB '*[^0-9a-f]*'),
    delete_set_digest TEXT NOT NULL CHECK (length(delete_set_digest) = 64 AND delete_set_digest NOT GLOB '*[^0-9a-f]*'),
    security_decision TEXT NOT NULL CHECK (security_decision IN ('allow_clean', 'admin_override')),
    security_decision_digest TEXT NOT NULL CHECK (length(security_decision_digest) = 64 AND security_decision_digest NOT GLOB '*[^0-9a-f]*'),
    security_finding_set_digest TEXT NOT NULL CHECK (length(security_finding_set_digest) = 64 AND security_finding_set_digest NOT GLOB '*[^0-9a-f]*'),
    security_policy_revision TEXT NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 64),
    security_override_binding_digest TEXT NOT NULL DEFAULT '' CHECK (security_override_binding_digest = ''
        OR (length(security_override_binding_digest) = 64 AND security_override_binding_digest NOT GLOB '*[^0-9a-f]*')),
    estimated_items INTEGER NOT NULL DEFAULT 0 CHECK (estimated_items >= 0),
    estimated_bytes INTEGER NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    authority_grant_id TEXT NOT NULL CHECK (length(authority_grant_id) = 32 AND authority_grant_id NOT GLOB '*[^0-9a-f]*'),
    authority_category TEXT NOT NULL CHECK (authority_category = 'write'),
    authority_binding_digest TEXT NOT NULL CHECK (length(authority_binding_digest) = 64 AND authority_binding_digest NOT GLOB '*[^0-9a-f]*'),
    authority_expires_at DATETIME NOT NULL,
    authority_consumed_at DATETIME NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'verifying', 'succeeded', 'degraded', 'needs_attention', 'failed', 'cancel_requested', 'canceled')),
    failure_category TEXT NOT NULL DEFAULT '' CHECK (length(failure_category) <= 64),
    transition_revision INTEGER NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    workspace_phase TEXT NOT NULL CHECK (workspace_phase IN ('none', 'reserved', 'marker_created', 'writing', 'sealed', 'published', 'cleanup_due', 'workspace_cleaned')),
    encrypted_workspace_relative_locator TEXT NOT NULL DEFAULT '',
    workspace_binding_digest TEXT NOT NULL DEFAULT '' CHECK (workspace_binding_digest = ''
        OR (length(workspace_binding_digest) = 64 AND workspace_binding_digest NOT GLOB '*[^0-9a-f]*')),
    workspace_marker_binding_digest TEXT NOT NULL DEFAULT '' CHECK (workspace_marker_binding_digest = ''
        OR (length(workspace_marker_binding_digest) = 64 AND workspace_marker_binding_digest NOT GLOB '*[^0-9a-f]*')),
    workspace_owner TEXT NOT NULL DEFAULT '' CHECK (length(workspace_owner) <= 64),
    workspace_fence INTEGER NOT NULL DEFAULT 0 CHECK (workspace_fence >= 0),
    workspace_marker_validation_attempt_id TEXT NOT NULL DEFAULT '' CHECK (
        workspace_marker_validation_attempt_id = ''
        OR (length(workspace_marker_validation_attempt_id) = 32
            AND workspace_marker_validation_attempt_id NOT GLOB '*[^0-9a-f]*')
    ),
    workspace_marker_validation_attempt_fence INTEGER NOT NULL DEFAULT 0
        CHECK (workspace_marker_validation_attempt_fence >= 0),
    workspace_marker_validation_node_fence INTEGER NOT NULL DEFAULT 0
        CHECK (workspace_marker_validation_node_fence >= 0),
    workspace_cleanup_phase TEXT NOT NULL DEFAULT 'claimed'
        CHECK (workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted', 'tombstoned')),
    workspace_cleanup_owner TEXT NOT NULL DEFAULT '' CHECK (length(workspace_cleanup_owner) <= 64),
    workspace_cleanup_lease_expires_at DATETIME,
    workspace_cleanup_fence INTEGER NOT NULL DEFAULT 0 CHECK (workspace_cleanup_fence >= 0),
    workspace_cleanup_node_lease_id TEXT CHECK (
        workspace_cleanup_node_lease_id IS NULL
        OR (length(workspace_cleanup_node_lease_id) = 32
            AND workspace_cleanup_node_lease_id NOT GLOB '*[^0-9a-f]*')
    ),
    workspace_cleanup_node_fence INTEGER NOT NULL DEFAULT 0 CHECK (workspace_cleanup_node_fence >= 0),
    workspace_cleanup_attempt INTEGER NOT NULL DEFAULT 0 CHECK (workspace_cleanup_attempt >= 0),
    plaintext_deadline DATETIME,
    target_mode TEXT NOT NULL CHECK (target_mode IN ('isolated', 'in_place')),
    target_node_id INTEGER NOT NULL,
    target_root_id TEXT NOT NULL CHECK (length(target_root_id) BETWEEN 1 AND 32),
    root_locator_digest TEXT NOT NULL CHECK (length(root_locator_digest) = 64 AND root_locator_digest NOT GLOB '*[^0-9a-f]*'),
    path_digest TEXT NOT NULL CHECK (length(path_digest) = 64 AND path_digest NOT GLOB '*[^0-9a-f]*'),
    target_chain_revision TEXT NOT NULL DEFAULT '' CHECK (length(target_chain_revision) <= 64),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
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
        OR
        (target_mode = 'isolated' AND (
            (workspace_phase = 'none' AND encrypted_workspace_relative_locator <> ''
                AND length(workspace_binding_digest) = 64
                AND workspace_marker_binding_digest = '' AND workspace_owner = ''
                AND workspace_fence = 0
                AND workspace_marker_validation_attempt_id = ''
                AND workspace_marker_validation_attempt_fence = 0
                AND workspace_marker_validation_node_fence = 0
                AND plaintext_deadline IS NULL)
            OR
            (workspace_phase = 'reserved'
                AND encrypted_workspace_relative_locator <> ''
                AND length(workspace_binding_digest) = 64
                AND length(workspace_marker_binding_digest) = 64 AND workspace_owner <> ''
                AND workspace_fence > 0
                AND workspace_marker_validation_attempt_id = ''
                AND workspace_marker_validation_attempt_fence = 0
                AND workspace_marker_validation_node_fence = 0
                AND plaintext_deadline IS NOT NULL)
            OR
            (workspace_phase IN ('marker_created', 'writing', 'sealed', 'published')
                AND encrypted_workspace_relative_locator <> ''
                AND length(workspace_binding_digest) = 64
                AND length(workspace_marker_binding_digest) = 64 AND workspace_owner <> ''
                AND workspace_fence > 0
                AND length(workspace_marker_validation_attempt_id) = 32
                AND workspace_marker_validation_attempt_fence > 0
                AND workspace_marker_validation_node_fence > 0
                AND plaintext_deadline IS NOT NULL)
            OR
            (workspace_phase IN ('cleanup_due', 'workspace_cleaned')
                AND encrypted_workspace_relative_locator <> ''
                AND length(workspace_binding_digest) = 64
                AND length(workspace_marker_binding_digest) = 64 AND workspace_owner <> ''
                AND workspace_fence > 0 AND plaintext_deadline IS NOT NULL
                AND (
                    (workspace_marker_validation_attempt_id = ''
                        AND workspace_marker_validation_attempt_fence = 0
                        AND workspace_marker_validation_node_fence = 0)
                    OR (length(workspace_marker_validation_attempt_id) = 32
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
    CHECK (
        (security_decision = 'admin_override' AND length(security_override_binding_digest) = 64)
        OR (security_decision = 'allow_clean' AND security_override_binding_digest = '')
    ),
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
        REFERENCES backup_asset_recovery_plans(id, target_mode, target_node_id) ON DELETE RESTRICT,
    FOREIGN KEY (target_node_id) REFERENCES nodes(id) ON DELETE RESTRICT,
    FOREIGN KEY (workspace_cleanup_node_lease_id, id)
        REFERENCES backup_asset_recovery_node_leases(id, job_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_jobs_plan ON backup_asset_recovery_jobs(plan_id);
CREATE UNIQUE INDEX idx_backup_asset_recovery_jobs_id_target_node
    ON backup_asset_recovery_jobs(id, target_node_id);
CREATE INDEX idx_backup_asset_recovery_jobs_claim
    ON backup_asset_recovery_jobs(state, updated_at, id);

CREATE TABLE backup_asset_recovery_job_items (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plan_id TEXT NOT NULL CHECK (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*'),
    plan_item_id TEXT CHECK (plan_item_id IS NULL
        OR (length(plan_item_id) = 32 AND plan_item_id NOT GLOB '*[^0-9a-f]*')),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    operation_kind TEXT NOT NULL CHECK (operation_kind IN ('create', 'overwrite', 'skip', 'delete')),
    target_path_digest TEXT NOT NULL CHECK (length(target_path_digest) = 64 AND target_path_digest NOT GLOB '*[^0-9a-f]*'),
    semantic_target_digest TEXT NOT NULL CHECK (length(semantic_target_digest) = 64 AND semantic_target_digest NOT GLOB '*[^0-9a-f]*'),
    target_object_digest TEXT NOT NULL CHECK (length(target_object_digest) = 64 AND target_object_digest NOT GLOB '*[^0-9a-f]*'),
    expected_prior_kind TEXT NOT NULL CHECK (expected_prior_kind IN ('absent', 'present')),
    expected_prior_digest TEXT NOT NULL DEFAULT '' CHECK (expected_prior_digest = ''
        OR (length(expected_prior_digest) = 64 AND expected_prior_digest NOT GLOB '*[^0-9a-f]*')),
    expected_post_identity_digest TEXT NOT NULL DEFAULT '' CHECK (expected_post_identity_digest = ''
        OR (length(expected_post_identity_digest) = 64 AND expected_post_identity_digest NOT GLOB '*[^0-9a-f]*')),
    expected_post_bytes INTEGER NOT NULL DEFAULT -1 CHECK (expected_post_bytes >= -1),
    expected_prior_bytes INTEGER NOT NULL DEFAULT -1 CHECK (expected_prior_bytes >= -1),
    encrypted_target_relative_locator TEXT NOT NULL CHECK (encrypted_target_relative_locator <> ''),
    target_locator_key_version INTEGER NOT NULL CHECK (target_locator_key_version > 0),
    target_locator_cipher_version INTEGER NOT NULL CHECK (target_locator_cipher_version > 0),
    display_class TEXT NOT NULL CHECK (display_class IN ('regular', 'directory', 'link', 'special')),
    estimated_bytes INTEGER NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'failed', 'skipped')),
    bytes_written INTEGER NOT NULL DEFAULT 0 CHECK (bytes_written >= 0),
    verified_size INTEGER NOT NULL DEFAULT 0 CHECK (verified_size >= 0),
    verified_digest TEXT NOT NULL DEFAULT '' CHECK (verified_digest = ''
        OR (length(verified_digest) = 64 AND verified_digest NOT GLOB '*[^0-9a-f]*')),
    failure_category TEXT NOT NULL DEFAULT '' CHECK (length(failure_category) <= 64),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
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

CREATE TRIGGER trg_backup_asset_recovery_job_items_insert_binding
BEFORE INSERT ON backup_asset_recovery_job_items
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
        FROM backup_asset_recovery_jobs AS job
        JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
        WHERE job.id = NEW.job_id
          AND job.plan_id = NEW.plan_id
          AND (
              NEW.operation_kind <> 'delete'
              OR (job.target_mode = 'in_place' AND plan.conflict_policy = 'exact_mirror')
          )
    ) THEN RAISE(ABORT, 'recovery job item insert binding mismatch') END;
END;

CREATE TRIGGER trg_backup_asset_recovery_job_items_binding_immutable
BEFORE UPDATE OF
    id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
    semantic_target_digest, target_object_digest,
    expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
    expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
    target_locator_key_version, target_locator_cipher_version, display_class,
    estimated_bytes, created_at
ON backup_asset_recovery_job_items
BEGIN
    SELECT RAISE(ABORT, 'recovery job item binding is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_job_items_projection
BEFORE UPDATE ON backup_asset_recovery_job_items
WHEN OLD.outcome <> '' OR NEW.outcome = ''
BEGIN
    SELECT RAISE(ABORT, 'recovery job item permits only pending-to-terminal projection');
END;

CREATE TABLE backup_asset_recovery_attempts (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*'),
    owner_id TEXT NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 64),
    fence INTEGER NOT NULL CHECK (fence > 0),
    state TEXT NOT NULL CHECK (state IN ('claimed', 'running', 'completed', 'failed', 'canceled', 'superseded', 'lost')),
    mutation_armed INTEGER NOT NULL DEFAULT 0 CHECK (mutation_armed IN (0, 1)),
    lease_expires_at DATETIME,
    heartbeat_at DATETIME,
    closed_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (id, job_id),
    UNIQUE (job_id, fence),
    CHECK (
        (state IN ('claimed', 'running') AND lease_expires_at IS NOT NULL AND heartbeat_at IS NOT NULL AND closed_at IS NULL)
        OR (state IN ('completed', 'failed', 'canceled', 'superseded', 'lost') AND closed_at IS NOT NULL)
    ),
    CHECK (mutation_armed = 0 OR state IN ('running', 'completed', 'failed', 'canceled', 'lost')),
    CHECK (heartbeat_at IS NULL OR heartbeat_at >= created_at),
    CHECK (closed_at IS NULL OR closed_at >= created_at),
    FOREIGN KEY (job_id) REFERENCES backup_asset_recovery_jobs(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_attempts_current
    ON backup_asset_recovery_attempts(job_id) WHERE state IN ('claimed', 'running');
CREATE INDEX idx_backup_asset_recovery_attempts_expiry
    ON backup_asset_recovery_attempts(state, lease_expires_at, updated_at);
CREATE TRIGGER trg_backup_asset_recovery_attempts_mutation_arm_monotonic
BEFORE UPDATE OF mutation_armed ON backup_asset_recovery_attempts
WHEN (OLD.mutation_armed = 1 AND NEW.mutation_armed = 0)
    OR (OLD.state IN ('completed', 'failed', 'canceled', 'superseded', 'lost')
        AND NEW.mutation_armed IS NOT OLD.mutation_armed)
BEGIN
    SELECT RAISE(ABORT, 'mutation_armed is monotonic');
END;
CREATE TRIGGER trg_backup_asset_recovery_attempts_integrity
BEFORE UPDATE ON backup_asset_recovery_attempts
WHEN NEW.id IS NOT OLD.id
    OR NEW.job_id IS NOT OLD.job_id
    OR (NEW.owner_id IS NOT OLD.owner_id AND NOT (
        OLD.owner_id = 'recovery-authorization'
        AND OLD.state = 'claimed' AND NEW.state = 'running'
        AND OLD.mutation_armed = 0 AND NEW.mutation_armed = 0
        AND EXISTS (
            SELECT 1 FROM backup_asset_recovery_jobs
            WHERE id = NEW.job_id AND state = 'queued'
        )
    ))
    OR NEW.fence IS NOT OLD.fence
    OR (OLD.state IN ('completed', 'failed', 'canceled', 'superseded', 'lost')
        AND NEW.state IS NOT OLD.state)
    OR (NEW.state IN ('claimed', 'running') AND EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs
        WHERE id = NEW.job_id AND state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'canceled')
    ))
BEGIN
    SELECT RAISE(ABORT, 'recovery attempt identity, terminal state, and terminal job barrier are immutable');
END;
CREATE TRIGGER trg_backup_asset_recovery_attempts_terminal_delete
BEFORE DELETE ON backup_asset_recovery_attempts
WHEN OLD.mutation_armed = 1
    OR OLD.state IN ('completed', 'failed', 'canceled', 'superseded', 'lost')
BEGIN
    SELECT RAISE(ABORT, 'terminal or mutation-armed recovery attempt cannot be deleted');
END;

CREATE TRIGGER trg_backup_asset_recovery_attempts_terminal_replay
BEFORE INSERT ON backup_asset_recovery_attempts
WHEN EXISTS (
    SELECT 1 FROM backup_asset_recovery_attempts
    WHERE id = NEW.id
      AND (mutation_armed = 1 OR state IN ('completed', 'failed', 'canceled', 'superseded', 'lost'))
)
BEGIN
    SELECT RAISE(ABORT, 'terminal or mutation-armed recovery attempt cannot be replaced');
END;

CREATE TABLE backup_asset_recovery_checkpoints (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*'),
    job_item_id TEXT NOT NULL DEFAULT '' CHECK (job_item_id = ''
        OR (length(job_item_id) = 32 AND job_item_id NOT GLOB '*[^0-9a-f]*')),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32 AND attempt_id NOT GLOB '*[^0-9a-f]*'),
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    phase TEXT NOT NULL CHECK (phase IN ('operation', 'operation_unresolved', 'delete_authority_required', 'delete_authority_consumed', 'verification', 'workspace_reserved')),
    authority_category TEXT NOT NULL DEFAULT '' CHECK (authority_category IN ('', 'write', 'exact_mirror_delete')),
    operation_digest TEXT NOT NULL DEFAULT '' CHECK (operation_digest = ''
        OR (length(operation_digest) = 64 AND operation_digest NOT GLOB '*[^0-9a-f]*')),
    prior_target_revision TEXT NOT NULL DEFAULT '' CHECK (length(prior_target_revision) <= 64),
    next_target_revision TEXT NOT NULL DEFAULT '' CHECK (length(next_target_revision) <= 64),
    unresolved_category TEXT NOT NULL DEFAULT '' CHECK (unresolved_category IN ('',
        'revision_disagreement', 'verification_mismatch', 'write_result_invalid', 'observation_invalid')),
    write_result_digest TEXT NOT NULL DEFAULT '' CHECK (write_result_digest = ''
        OR (length(write_result_digest) = 64 AND write_result_digest NOT GLOB '*[^0-9a-f]*')),
    write_target_revision TEXT NOT NULL DEFAULT '' CHECK (length(write_target_revision) <= 64),
    observation_digest TEXT NOT NULL DEFAULT '' CHECK (observation_digest = ''
        OR (length(observation_digest) = 64 AND observation_digest NOT GLOB '*[^0-9a-f]*')),
    observed_target_revision TEXT NOT NULL DEFAULT '' CHECK (length(observed_target_revision) <= 64),
    observed_presence TEXT NOT NULL DEFAULT '' CHECK (observed_presence IN ('', 'present', 'absent')),
    source_revalidation_outcome TEXT NOT NULL DEFAULT '' CHECK (source_revalidation_outcome IN ('', 'matched', 'drifted', 'failed')),
    node_fence INTEGER NOT NULL DEFAULT 0 CHECK (node_fence >= 0),
    attempt_fence INTEGER NOT NULL DEFAULT 0 CHECK (attempt_fence >= 0),
    plan_binding_digest TEXT NOT NULL CHECK (length(plan_binding_digest) = 64 AND plan_binding_digest NOT GLOB '*[^0-9a-f]*'),
    source_revision_digest TEXT NOT NULL CHECK (length(source_revision_digest) = 64 AND source_revision_digest NOT GLOB '*[^0-9a-f]*'),
    preflight_id TEXT NOT NULL CHECK (length(preflight_id) = 32 AND preflight_id NOT GLOB '*[^0-9a-f]*'),
    preflight_revision TEXT NOT NULL CHECK (length(preflight_revision) BETWEEN 1 AND 64),
    preflight_expires_at DATETIME NOT NULL,
    security_decision TEXT NOT NULL CHECK (security_decision IN ('allow_clean', 'admin_override')),
    security_decision_digest TEXT NOT NULL CHECK (length(security_decision_digest) = 64 AND security_decision_digest NOT GLOB '*[^0-9a-f]*'),
    security_finding_set_digest TEXT NOT NULL CHECK (length(security_finding_set_digest) = 64 AND security_finding_set_digest NOT GLOB '*[^0-9a-f]*'),
    security_policy_revision TEXT NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 64),
    authority_grant_id TEXT NOT NULL CHECK (length(authority_grant_id) = 32 AND authority_grant_id NOT GLOB '*[^0-9a-f]*'),
    job_authority_category TEXT NOT NULL CHECK (job_authority_category = 'write'),
    authority_binding_digest TEXT NOT NULL CHECK (length(authority_binding_digest) = 64 AND authority_binding_digest NOT GLOB '*[^0-9a-f]*'),
    authority_expires_at DATETIME NOT NULL,
    delete_node_revision TEXT NOT NULL DEFAULT '' CHECK (length(delete_node_revision) <= 64),
    delete_root_revision TEXT NOT NULL DEFAULT '' CHECK (length(delete_root_revision) <= 64),
    delete_authority_expires_at DATETIME,
    delete_grant_id TEXT NOT NULL DEFAULT '' CHECK (delete_grant_id = ''
        OR (length(delete_grant_id) = 32 AND delete_grant_id NOT GLOB '*[^0-9a-f]*')),
    delete_grant_binding_digest TEXT NOT NULL DEFAULT '' CHECK (delete_grant_binding_digest = ''
        OR (length(delete_grant_binding_digest) = 64 AND delete_grant_binding_digest NOT GLOB '*[^0-9a-f]*')),
    delete_grant_expires_at DATETIME,
    delete_grant_consumed_at DATETIME,
    created_at DATETIME NOT NULL,
    UNIQUE (job_id, sequence),
    CHECK (
        (phase = 'operation' AND authority_category = 'write' AND length(operation_digest) = 64
            AND prior_target_revision <> '' AND next_target_revision <> ''
            AND node_fence > 0 AND attempt_fence > 0
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
        OR (phase IN ('verification', 'workspace_reserved') AND authority_category = ''
            AND operation_digest = '' AND prior_target_revision = '' AND next_target_revision = ''
            AND node_fence = 0 AND attempt_fence = 0 AND delete_node_revision = ''
            AND delete_root_revision = '' AND delete_authority_expires_at IS NULL
            AND delete_grant_id = '' AND delete_grant_binding_digest = ''
            AND delete_grant_expires_at IS NULL AND delete_grant_consumed_at IS NULL)
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
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT CHECK (job_id IS NULL OR (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*')),
    kind TEXT NOT NULL CHECK (kind IN ('verification', 'difference', 'failure', 'authorization_receipt', 'schema_use_latch', 'scheduler_state')),
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'degraded', 'failed', 'needs_attention')),
    summary_digest TEXT NOT NULL DEFAULT '' CHECK (summary_digest = ''
        OR (length(summary_digest) = 64 AND summary_digest NOT GLOB '*[^0-9a-f]*')),
    difference_count INTEGER NOT NULL DEFAULT 0 CHECK (difference_count >= 0),
    verified_at DATETIME,
    plan_id TEXT CHECK (plan_id IS NULL OR (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*')),
    checkpoint_id TEXT CHECK (checkpoint_id IS NULL OR (length(checkpoint_id) = 32 AND checkpoint_id NOT GLOB '*[^0-9a-f]*')),
    grant_id TEXT CHECK (grant_id IS NULL OR (length(grant_id) = 32 AND grant_id NOT GLOB '*[^0-9a-f]*')),
    attempt_id TEXT CHECK (attempt_id IS NULL OR (length(attempt_id) = 32 AND attempt_id NOT GLOB '*[^0-9a-f]*')),
    source_lease_id TEXT,
    node_lease_id TEXT CHECK (node_lease_id IS NULL OR (length(node_lease_id) = 32 AND node_lease_id NOT GLOB '*[^0-9a-f]*')),
    requester_id INTEGER,
    operation TEXT NOT NULL DEFAULT '' CHECK (operation IN ('', 'security_override', 'write_authorize', 'exact_mirror_delete_authorize', 'execute')),
    category TEXT NOT NULL DEFAULT '' CHECK (category IN ('', 'security_override', 'write', 'exact_mirror_delete', 'execute')),
    endpoint TEXT NOT NULL DEFAULT '' CHECK (length(endpoint) <= 96),
    idempotency_key_digest TEXT NOT NULL DEFAULT '' CHECK (idempotency_key_digest = ''
        OR (length(idempotency_key_digest) = 64 AND idempotency_key_digest NOT GLOB '*[^0-9a-f]*')),
    intent_digest TEXT NOT NULL DEFAULT '' CHECK (intent_digest = ''
        OR (length(intent_digest) = 64 AND intent_digest NOT GLOB '*[^0-9a-f]*')),
    step_up_jti_digest TEXT NOT NULL DEFAULT '' CHECK (step_up_jti_digest = ''
        OR (length(step_up_jti_digest) = 64 AND step_up_jti_digest NOT GLOB '*[^0-9a-f]*')),
    presenting_session_digest TEXT NOT NULL DEFAULT '' CHECK (presenting_session_digest = ''
        OR (length(presenting_session_digest) = 64 AND presenting_session_digest NOT GLOB '*[^0-9a-f]*')),
    presenting_session_user_id INTEGER,
    presenting_session_role TEXT NOT NULL DEFAULT '' CHECK (presenting_session_role IN ('', 'admin')),
    presenting_session_token_version INTEGER NOT NULL DEFAULT 0 CHECK (presenting_session_token_version >= 0),
    proof_expires_at DATETIME,
    presenting_session_expires_at DATETIME,
    replay_expires_at DATETIME,
    expected_plan_transition_revision INTEGER NOT NULL DEFAULT 0 CHECK (expected_plan_transition_revision >= 0),
    result_plan_transition_revision INTEGER NOT NULL DEFAULT 0 CHECK (result_plan_transition_revision >= 0),
    grant_binding_digest TEXT NOT NULL DEFAULT '' CHECK (grant_binding_digest = ''
        OR (length(grant_binding_digest) = 64 AND grant_binding_digest NOT GLOB '*[^0-9a-f]*')),
    source_lease_binding_digest TEXT NOT NULL DEFAULT '' CHECK (source_lease_binding_digest = ''
        OR (length(source_lease_binding_digest) = 64 AND source_lease_binding_digest NOT GLOB '*[^0-9a-f]*')),
    node_lease_fence INTEGER NOT NULL DEFAULT 0 CHECK (node_lease_fence >= 0),
    scheduler_scope TEXT NOT NULL DEFAULT '' CHECK (scheduler_scope IN ('', 'claim', 'takeover')),
    scheduler_cursor_at DATETIME,
    scheduler_cursor_id TEXT NOT NULL DEFAULT '' CHECK (scheduler_cursor_id = ''
        OR (length(scheduler_cursor_id) = 32 AND scheduler_cursor_id NOT GLOB '*[^0-9a-f]*')),
    scheduler_high_water_at DATETIME,
    scheduler_high_water_id TEXT NOT NULL DEFAULT '' CHECK (scheduler_high_water_id = ''
        OR (length(scheduler_high_water_id) = 32 AND scheduler_high_water_id NOT GLOB '*[^0-9a-f]*')),
    scheduler_revision INTEGER NOT NULL DEFAULT 0 CHECK (scheduler_revision >= 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((scheduler_cursor_at IS NULL AND scheduler_cursor_id = '')
        OR (scheduler_cursor_at IS NOT NULL AND scheduler_cursor_id <> '')),
    CHECK ((scheduler_high_water_at IS NULL AND scheduler_high_water_id = '')
        OR (scheduler_high_water_at IS NOT NULL AND scheduler_high_water_id <> '')),
    CHECK (scheduler_cursor_at IS NULL OR (scheduler_high_water_at IS NOT NULL AND
        (scheduler_cursor_at < scheduler_high_water_at
            OR (scheduler_cursor_at = scheduler_high_water_at AND scheduler_cursor_id <= scheduler_high_water_id)))),
    CHECK (
        (id = '00000000000000000000000000000069' AND kind = 'schema_use_latch'
            AND job_id IS NULL AND outcome = '' AND summary_digest = ''
            AND difference_count = 0 AND verified_at IS NULL
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
        OR
        (id <> '00000000000000000000000000000069' AND kind IN ('verification', 'difference', 'failure')
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
        OR
        (id <> '00000000000000000000000000000069' AND kind = 'authorization_receipt'
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
                    AND grant_binding_digest = '' AND source_lease_binding_digest = ''
                    AND node_lease_fence = 0)
                OR
                (operation = 'write_authorize' AND category = 'write'
                    AND endpoint = '/api/v1/recovery-plans/:id/write-authorizations'
                    AND job_id IS NULL AND checkpoint_id IS NULL AND grant_id IS NOT NULL
                    AND attempt_id IS NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision + 1
                    AND length(grant_binding_digest) = 64 AND source_lease_binding_digest = ''
                    AND node_lease_fence = 0)
                OR
                (operation = 'exact_mirror_delete_authorize' AND category = 'exact_mirror_delete'
                    AND endpoint = '/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations'
                    AND job_id IS NOT NULL AND checkpoint_id IS NOT NULL AND grant_id IS NOT NULL
                    AND attempt_id IS NOT NULL AND source_lease_id IS NULL AND node_lease_id IS NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision
                    AND length(grant_binding_digest) = 64 AND source_lease_binding_digest = ''
                    AND node_lease_fence = 0)
                OR
                (operation = 'execute' AND category = 'execute'
                    AND endpoint = '/api/v1/recovery-plans/:id/execute'
                    AND job_id IS NOT NULL AND checkpoint_id IS NULL AND grant_id IS NOT NULL
                    AND attempt_id IS NOT NULL AND source_lease_id IS NOT NULL AND node_lease_id IS NOT NULL
                    AND result_plan_transition_revision = expected_plan_transition_revision + 1
                    AND length(grant_binding_digest) = 64 AND length(source_lease_binding_digest) = 64
                    AND node_lease_fence > 0)
            )
            AND scheduler_scope = '' AND scheduler_cursor_at IS NULL AND scheduler_cursor_id = ''
            AND scheduler_high_water_at IS NULL AND scheduler_high_water_id = '' AND scheduler_revision = 0)
        OR
        (kind = 'scheduler_state'
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
    CHECK (created_at <= updated_at),
    FOREIGN KEY (job_id) REFERENCES backup_asset_recovery_jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (plan_id) REFERENCES backup_asset_recovery_plans(id) ON DELETE RESTRICT,
    FOREIGN KEY (checkpoint_id) REFERENCES backup_asset_recovery_checkpoints(id) ON DELETE RESTRICT,
    FOREIGN KEY (grant_id) REFERENCES backup_asset_recovery_grants(id) ON DELETE RESTRICT,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_recovery_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (source_lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT,
    FOREIGN KEY (node_lease_id) REFERENCES backup_asset_recovery_node_leases(id) ON DELETE RESTRICT,
    FOREIGN KEY (requester_id) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (presenting_session_user_id) REFERENCES users(id) ON DELETE RESTRICT
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
CREATE TRIGGER trg_backup_asset_recovery_evidence_latch_update
BEFORE UPDATE ON backup_asset_recovery_evidence
WHEN OLD.id = '00000000000000000000000000000069' AND OLD.kind = 'schema_use_latch'
BEGIN
    SELECT RAISE(ABORT, 'schema_use_latch is immutable');
END;
CREATE TRIGGER trg_backup_asset_recovery_evidence_latch_delete
BEFORE DELETE ON backup_asset_recovery_evidence
WHEN OLD.id = '00000000000000000000000000000069' AND OLD.kind = 'schema_use_latch'
BEGIN
    SELECT RAISE(ABORT, 'schema_use_latch is permanent');
END;
CREATE TRIGGER trg_backup_asset_recovery_evidence_scheduler_update
BEFORE UPDATE ON backup_asset_recovery_evidence
WHEN OLD.kind = 'scheduler_state' AND (
    NEW.id IS NOT OLD.id OR NEW.job_id IS NOT OLD.job_id OR NEW.kind IS NOT OLD.kind
    OR NEW.outcome IS NOT OLD.outcome OR NEW.summary_digest IS NOT OLD.summary_digest
    OR NEW.difference_count IS NOT OLD.difference_count OR NEW.verified_at IS NOT OLD.verified_at
    OR NEW.plan_id IS NOT OLD.plan_id OR NEW.checkpoint_id IS NOT OLD.checkpoint_id
    OR NEW.grant_id IS NOT OLD.grant_id OR NEW.attempt_id IS NOT OLD.attempt_id
    OR NEW.source_lease_id IS NOT OLD.source_lease_id OR NEW.node_lease_id IS NOT OLD.node_lease_id
    OR NEW.requester_id IS NOT OLD.requester_id OR NEW.operation IS NOT OLD.operation
    OR NEW.category IS NOT OLD.category OR NEW.endpoint IS NOT OLD.endpoint
    OR NEW.idempotency_key_digest IS NOT OLD.idempotency_key_digest
    OR NEW.intent_digest IS NOT OLD.intent_digest OR NEW.step_up_jti_digest IS NOT OLD.step_up_jti_digest
    OR NEW.presenting_session_digest IS NOT OLD.presenting_session_digest
    OR NEW.presenting_session_user_id IS NOT OLD.presenting_session_user_id
    OR NEW.presenting_session_role IS NOT OLD.presenting_session_role
    OR NEW.presenting_session_token_version IS NOT OLD.presenting_session_token_version
    OR NEW.proof_expires_at IS NOT OLD.proof_expires_at
    OR NEW.presenting_session_expires_at IS NOT OLD.presenting_session_expires_at
    OR NEW.replay_expires_at IS NOT OLD.replay_expires_at
    OR NEW.expected_plan_transition_revision IS NOT OLD.expected_plan_transition_revision
    OR NEW.result_plan_transition_revision IS NOT OLD.result_plan_transition_revision
    OR NEW.grant_binding_digest IS NOT OLD.grant_binding_digest
    OR NEW.source_lease_binding_digest IS NOT OLD.source_lease_binding_digest
    OR NEW.node_lease_fence IS NOT OLD.node_lease_fence
    OR NEW.scheduler_scope IS NOT OLD.scheduler_scope OR NEW.created_at IS NOT OLD.created_at
    OR NEW.scheduler_revision <> OLD.scheduler_revision + 1 OR NEW.updated_at < OLD.updated_at
)
BEGIN
    SELECT RAISE(ABORT, 'recovery scheduler state update is invalid');
END;
CREATE TRIGGER trg_backup_asset_recovery_evidence_scheduler_delete
BEFORE DELETE ON backup_asset_recovery_evidence
WHEN OLD.kind = 'scheduler_state'
BEGIN
    SELECT RAISE(ABORT, 'recovery scheduler state is permanent');
END;
CREATE TRIGGER trg_backup_asset_recovery_evidence_receipt_update
BEFORE UPDATE ON backup_asset_recovery_evidence
WHEN OLD.kind = 'authorization_receipt'
BEGIN
    SELECT RAISE(ABORT, 'authorization receipt is immutable');
END;
CREATE TRIGGER trg_backup_asset_recovery_evidence_receipt_delete
BEFORE DELETE ON backup_asset_recovery_evidence
WHEN OLD.kind = 'authorization_receipt' AND OLD.replay_expires_at > CURRENT_TIMESTAMP
BEGIN
    SELECT RAISE(ABORT, 'authorization receipt replay window remains active');
END;
CREATE TRIGGER trg_backup_asset_recovery_evidence_receipt_insert
BEFORE INSERT ON backup_asset_recovery_evidence
WHEN NEW.kind = 'authorization_receipt' AND (
    NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_plans AS plan_row
        WHERE plan_row.id = NEW.plan_id
          AND plan_row.requester_id = NEW.requester_id
    )
    OR (NEW.grant_id IS NOT NULL AND NOT EXISTS (
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
    ))
    OR (NEW.job_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs AS job_row
        WHERE job_row.id = NEW.job_id AND job_row.plan_id = NEW.plan_id
    ))
    OR (NEW.attempt_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_attempts AS attempt_row
        WHERE attempt_row.id = NEW.attempt_id AND attempt_row.job_id = NEW.job_id
    ))
    OR (NEW.checkpoint_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_checkpoints AS checkpoint_row
        WHERE checkpoint_row.id = NEW.checkpoint_id AND checkpoint_row.job_id = NEW.job_id
          AND checkpoint_row.attempt_id = NEW.attempt_id
    ))
    OR (NEW.node_lease_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_node_leases AS node_lease
        WHERE node_lease.id = NEW.node_lease_id AND node_lease.job_id = NEW.job_id
          AND node_lease.attempt_id = NEW.attempt_id AND node_lease.fence = NEW.node_lease_fence
    ))
    OR (NEW.operation = 'execute' AND (
        NOT EXISTS (
            SELECT 1
            FROM recovery_point_leases AS source_lease
            JOIN backup_asset_recovery_plans AS plan_row
              ON plan_row.id = NEW.plan_id AND plan_row.recovery_point_id = source_lease.recovery_point_id
            WHERE source_lease.id = NEW.source_lease_id
              AND source_lease.holder_type = 'recovery_job'
              AND source_lease.owner_id = NEW.job_id
              AND source_lease.attempt_id = NEW.attempt_id
        )
        OR 1 <> (
            SELECT COUNT(*) FROM recovery_point_leases AS source_lease
            WHERE source_lease.holder_type = 'recovery_job'
              AND source_lease.owner_id = NEW.job_id
              AND source_lease.attempt_id = NEW.attempt_id
        )
    ))
)
BEGIN
    SELECT RAISE(ABORT, 'authorization receipt effect linkage is invalid');
END;

CREATE TABLE backup_asset_recovery_node_leases (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    node_id INTEGER NOT NULL,
    holder_kind TEXT NOT NULL CHECK (holder_kind IN ('recovery_job', 'recovery_cleanup')),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*'),
    attempt_id TEXT CHECK (attempt_id IS NULL OR (length(attempt_id) = 32 AND attempt_id NOT GLOB '*[^0-9a-f]*')),
    owner_id TEXT NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 64),
    fence INTEGER NOT NULL CHECK (fence > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'released', 'lost', 'expired')),
    lease_expires_at DATETIME NOT NULL,
    released_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (id, job_id),
    CHECK (
        (holder_kind = 'recovery_job' AND attempt_id IS NOT NULL)
        OR (holder_kind = 'recovery_cleanup' AND attempt_id IS NULL)
    ),
    CHECK (
        (state = 'active' AND released_at IS NULL)
        OR (state IN ('released', 'lost', 'expired') AND released_at IS NOT NULL)
    ),
    CHECK (lease_expires_at >= created_at AND created_at <= updated_at),
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT,
    FOREIGN KEY (job_id) REFERENCES backup_asset_recovery_jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id, node_id)
        REFERENCES backup_asset_recovery_jobs(id, target_node_id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, job_id) REFERENCES backup_asset_recovery_attempts(id, job_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX idx_backup_asset_recovery_node_leases_active_node
    ON backup_asset_recovery_node_leases(node_id) WHERE state = 'active';
CREATE INDEX idx_backup_asset_recovery_node_leases_claim
    ON backup_asset_recovery_node_leases(state, lease_expires_at, node_id);
CREATE TRIGGER trg_backup_asset_recovery_jobs_workspace_cleanup_insert
BEFORE INSERT ON backup_asset_recovery_jobs
WHEN NEW.workspace_cleanup_phase <> 'claimed'
    OR NEW.workspace_cleanup_owner <> ''
    OR NEW.workspace_cleanup_lease_expires_at IS NOT NULL
    OR NEW.workspace_cleanup_fence <> 0
    OR NEW.workspace_cleanup_node_lease_id IS NOT NULL
    OR NEW.workspace_cleanup_node_fence <> 0
    OR NEW.workspace_cleanup_attempt <> 0
BEGIN
    SELECT RAISE(ABORT, 'recovery workspace cleanup must start neutral');
END;

CREATE TRIGGER trg_backup_asset_recovery_jobs_workspace_cleanup_transition
BEFORE UPDATE OF workspace_cleanup_phase, workspace_cleanup_owner,
    workspace_cleanup_lease_expires_at, workspace_cleanup_fence,
    workspace_cleanup_node_lease_id, workspace_cleanup_node_fence,
    workspace_cleanup_attempt
ON backup_asset_recovery_jobs
WHEN (
        NEW.workspace_cleanup_phase IS NOT OLD.workspace_cleanup_phase
        OR NEW.workspace_cleanup_owner IS NOT OLD.workspace_cleanup_owner
        OR NEW.workspace_cleanup_lease_expires_at IS NOT OLD.workspace_cleanup_lease_expires_at
        OR NEW.workspace_cleanup_fence IS NOT OLD.workspace_cleanup_fence
        OR NEW.workspace_cleanup_node_lease_id IS NOT OLD.workspace_cleanup_node_lease_id
        OR NEW.workspace_cleanup_node_fence IS NOT OLD.workspace_cleanup_node_fence
        OR NEW.workspace_cleanup_attempt IS NOT OLD.workspace_cleanup_attempt
    ) AND NOT (
        OLD.workspace_phase = 'cleanup_due' AND (
            (
                NEW.workspace_phase = 'cleanup_due'
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
                )
            )
            OR (
                NEW.workspace_phase = 'cleanup_due'
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
                )
            )
            OR (
                NEW.workspace_phase = 'cleanup_due'
                AND OLD.workspace_cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted')
                AND (
                    NEW.workspace_cleanup_phase = OLD.workspace_cleanup_phase
                    OR NEW.workspace_cleanup_phase = CASE OLD.workspace_cleanup_phase
                        WHEN 'claimed' THEN 'revoked'
                        WHEN 'revoked' THEN 'drained'
                        WHEN 'drained' THEN 'validated'
                        WHEN 'validated' THEN 'delete_started'
                        WHEN 'delete_started' THEN 'deleted'
                    END
                )
                AND NEW.workspace_cleanup_owner = OLD.workspace_cleanup_owner
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
                )
            )
            OR (
                NEW.workspace_phase = 'cleanup_due'
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
                AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt
            )
            OR (
                NEW.workspace_phase = 'workspace_cleaned'
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
                AND NEW.workspace_cleanup_attempt = OLD.workspace_cleanup_attempt
            )
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'recovery workspace cleanup transition is invalid');
END;
CREATE TABLE backup_asset_recovery_result_sets (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL UNIQUE CHECK (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('ready', 'revoking', 'cleaned', 'cleanup_failed')),
    marker_binding_digest TEXT NOT NULL CHECK (length(marker_binding_digest) = 64 AND marker_binding_digest NOT GLOB '*[^0-9a-f]*'),
    plaintext_deadline DATETIME NOT NULL,
    hard_deadline DATETIME NOT NULL,
    cleanup_phase TEXT NOT NULL CHECK (cleanup_phase IN ('claimed', 'revoked', 'drained', 'validated', 'delete_started', 'deleted', 'tombstoned')),
    cleanup_owner TEXT NOT NULL DEFAULT '' CHECK (length(cleanup_owner) <= 64),
    cleanup_lease_expires_at DATETIME,
    cleanup_fence INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_fence >= 0),
    node_lease_id TEXT CHECK (node_lease_id IS NULL OR (length(node_lease_id) = 32 AND node_lease_id NOT GLOB '*[^0-9a-f]*')),
    node_fence INTEGER NOT NULL DEFAULT 0 CHECK (node_fence >= 0),
    cleanup_attempt INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_attempt >= 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (id, job_id),
    CHECK (created_at < plaintext_deadline AND plaintext_deadline <= hard_deadline),
    CHECK (
        (state = 'ready' AND cleanup_owner = '' AND cleanup_lease_expires_at IS NULL
            AND cleanup_fence = 0 AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt = 0
            AND cleanup_phase = 'claimed')
        OR (state = 'revoking' AND cleanup_owner <> '' AND cleanup_lease_expires_at IS NOT NULL
            AND cleanup_fence > 0 AND node_lease_id IS NOT NULL AND node_fence > 0 AND cleanup_attempt > 0)
        OR (state = 'cleanup_failed' AND cleanup_owner = '' AND cleanup_lease_expires_at IS NULL
            AND cleanup_fence > 0 AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt > 0)
        OR (state = 'cleaned' AND cleanup_phase = 'tombstoned' AND cleanup_owner = ''
            AND cleanup_lease_expires_at IS NULL AND cleanup_fence > 0
            AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt > 0)
    ),
    CHECK (created_at <= updated_at),
    FOREIGN KEY (job_id) REFERENCES backup_asset_recovery_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (node_lease_id, job_id) REFERENCES backup_asset_recovery_node_leases(id, job_id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_recovery_result_sets_expiry
    ON backup_asset_recovery_result_sets(state, plaintext_deadline, hard_deadline);
CREATE INDEX idx_backup_asset_recovery_result_sets_cleanup
    ON backup_asset_recovery_result_sets(state, cleanup_lease_expires_at, updated_at);

CREATE TABLE backup_asset_recovery_results (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    result_set_id TEXT NOT NULL CHECK (length(result_set_id) = 32 AND result_set_id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*'),
    result_kind TEXT NOT NULL CHECK (result_kind IN ('regular_file', 'verification_report')),
    classification TEXT NOT NULL CHECK (classification IN ('non_secret', 'secret', 'unknown')),
    classification_revision INTEGER NOT NULL CHECK (classification_revision > 0),
    classification_source_revision INTEGER NOT NULL CHECK (classification_source_revision > 0),
    encrypted_relative_locator TEXT NOT NULL DEFAULT '',
    locator_digest TEXT NOT NULL CHECK (length(locator_digest) = 64 AND locator_digest NOT GLOB '*[^0-9a-f]*'),
    size INTEGER NOT NULL DEFAULT 0 CHECK (size >= 0),
    content_digest TEXT NOT NULL DEFAULT '' CHECK (content_digest = ''
        OR (length(content_digest) = 64 AND content_digest NOT GLOB '*[^0-9a-f]*')),
    modified_at DATETIME,
    created_at DATETIME NOT NULL,
    UNIQUE (id, job_id),
    UNIQUE (result_set_id, locator_digest),
    CHECK (encrypted_relative_locator <> ''),
    FOREIGN KEY (result_set_id, job_id)
        REFERENCES backup_asset_recovery_result_sets(id, job_id) ON DELETE CASCADE
);
CREATE INDEX idx_backup_asset_recovery_results_job
    ON backup_asset_recovery_results(job_id, id);

CREATE TABLE backup_asset_recovery_grants (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plan_id TEXT NOT NULL CHECK (length(plan_id) = 32 AND plan_id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT CHECK (job_id IS NULL OR (length(job_id) = 32 AND job_id NOT GLOB '*[^0-9a-f]*')),
    authority_category TEXT NOT NULL CHECK (authority_category IN ('write', 'exact_mirror_delete')),
    grant_hash TEXT NOT NULL CHECK (length(grant_hash) = 64 AND grant_hash NOT GLOB '*[^0-9a-f]*'),
    actor_user_id INTEGER NOT NULL,
    actor_session_id TEXT NOT NULL CHECK (length(actor_session_id) BETWEEN 1 AND 64),
    binding_digest TEXT NOT NULL CHECK (length(binding_digest) = 64 AND binding_digest NOT GLOB '*[^0-9a-f]*'),
    encrypted_reason TEXT NOT NULL DEFAULT '',
    delete_checkpoint_id TEXT CHECK (delete_checkpoint_id IS NULL
        OR (length(delete_checkpoint_id) = 32 AND delete_checkpoint_id NOT GLOB '*[^0-9a-f]*')),
    delete_set_digest TEXT NOT NULL DEFAULT '' CHECK (delete_set_digest = ''
        OR (length(delete_set_digest) = 64 AND delete_set_digest NOT GLOB '*[^0-9a-f]*')),
    delete_target_revision TEXT NOT NULL DEFAULT '' CHECK (length(delete_target_revision) <= 64),
    delete_attempt_id TEXT CHECK (delete_attempt_id IS NULL
        OR (length(delete_attempt_id) = 32 AND delete_attempt_id NOT GLOB '*[^0-9a-f]*')),
    delete_attempt_fence INTEGER NOT NULL DEFAULT 0 CHECK (delete_attempt_fence >= 0),
    delete_node_fence INTEGER NOT NULL DEFAULT 0 CHECK (delete_node_fence >= 0),
    expires_at DATETIME NOT NULL,
    consumed_at DATETIME,
    revoked_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (
        (authority_category = 'write' AND job_id IS NULL AND delete_checkpoint_id IS NULL
            AND delete_set_digest = '' AND delete_target_revision = '' AND delete_attempt_id IS NULL
            AND delete_attempt_fence = 0 AND delete_node_fence = 0)
        OR (authority_category = 'exact_mirror_delete' AND job_id IS NOT NULL
            AND delete_checkpoint_id IS NOT NULL AND length(delete_set_digest) = 64
            AND delete_target_revision <> '' AND delete_attempt_id IS NOT NULL
            AND delete_attempt_fence > 0 AND delete_node_fence > 0)
    ),
    CHECK (encrypted_reason <> '' AND expires_at > created_at AND created_at <= updated_at),
    CHECK (consumed_at IS NULL OR (consumed_at >= created_at AND consumed_at <= expires_at)),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CHECK (consumed_at IS NULL OR revoked_at IS NULL),
    FOREIGN KEY (plan_id) REFERENCES backup_asset_recovery_plans(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id, plan_id) REFERENCES backup_asset_recovery_jobs(id, plan_id) ON DELETE RESTRICT,
    FOREIGN KEY (delete_checkpoint_id) REFERENCES backup_asset_recovery_checkpoints(id) ON DELETE RESTRICT,
    FOREIGN KEY (delete_attempt_id, job_id) REFERENCES backup_asset_recovery_attempts(id, job_id) ON DELETE RESTRICT,
    FOREIGN KEY (actor_user_id) REFERENCES users(id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_recovery_grants_plan_category_expiry
    ON backup_asset_recovery_grants(plan_id, authority_category, expires_at);
CREATE INDEX idx_backup_asset_recovery_grants_job_category
    ON backup_asset_recovery_grants(job_id, authority_category, consumed_at);
INSERT INTO backup_asset_recovery_evidence
    (id, kind, scheduler_scope, scheduler_revision, created_at, updated_at)
VALUES
    ('0000000000000000000000000000006a', 'scheduler_state', 'claim', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('0000000000000000000000000000006b', 'scheduler_state', 'takeover', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

CREATE TRIGGER trg_backup_asset_recovery_grants_terminal
BEFORE UPDATE ON backup_asset_recovery_grants
WHEN OLD.consumed_at IS NOT NULL
    OR OLD.revoked_at IS NOT NULL
    OR NEW.id IS NOT OLD.id
    OR NEW.plan_id IS NOT OLD.plan_id
    OR NEW.job_id IS NOT OLD.job_id
    OR NEW.authority_category IS NOT OLD.authority_category
    OR NEW.grant_hash IS NOT OLD.grant_hash
    OR NEW.actor_user_id IS NOT OLD.actor_user_id
    OR NEW.actor_session_id IS NOT OLD.actor_session_id
    OR NEW.binding_digest IS NOT OLD.binding_digest
    OR NEW.encrypted_reason IS NOT OLD.encrypted_reason
    OR NEW.delete_checkpoint_id IS NOT OLD.delete_checkpoint_id
    OR NEW.delete_set_digest IS NOT OLD.delete_set_digest
    OR NEW.delete_target_revision IS NOT OLD.delete_target_revision
    OR NEW.delete_attempt_id IS NOT OLD.delete_attempt_id
    OR NEW.delete_attempt_fence IS NOT OLD.delete_attempt_fence
    OR NEW.delete_node_fence IS NOT OLD.delete_node_fence
    OR NEW.expires_at IS NOT OLD.expires_at
    OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'recovery grant terminal state is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_grants_terminal_delete
BEFORE DELETE ON backup_asset_recovery_grants
WHEN OLD.consumed_at IS NOT NULL OR OLD.revoked_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'terminal recovery grant cannot be deleted');
END;

CREATE TRIGGER trg_backup_asset_recovery_grants_terminal_replay
BEFORE INSERT ON backup_asset_recovery_grants
WHEN EXISTS (
    SELECT 1 FROM backup_asset_recovery_grants
    WHERE id = NEW.id AND (consumed_at IS NOT NULL OR revoked_at IS NOT NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'terminal recovery grant cannot be replaced');
END;

CREATE TRIGGER trg_backup_asset_recovery_grants_delete_binding_insert
BEFORE INSERT ON backup_asset_recovery_grants
WHEN NEW.authority_category = 'exact_mirror_delete'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
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
    ) THEN RAISE(ABORT, 'exact-mirror delete grant binding mismatch') END;
END;

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
WHEN (
        OLD.state IN ('authorized', 'executed')
        OR OLD.security_decision = 'admin_override'
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_grants WHERE plan_id = OLD.id)
        OR EXISTS (SELECT 1 FROM backup_asset_recovery_jobs WHERE plan_id = OLD.id)
    ) AND (
        NEW.id IS NOT OLD.id
        OR NEW.requester_id IS NOT OLD.requester_id
        OR NEW.endpoint IS NOT OLD.endpoint
        OR NEW.idempotency_key_digest IS NOT OLD.idempotency_key_digest
        OR NEW.repository_id IS NOT OLD.repository_id
        OR NEW.recovery_point_id IS NOT OLD.recovery_point_id
        OR NEW.source_revision_digest IS NOT OLD.source_revision_digest
        OR NEW.source_revision_kind IS NOT OLD.source_revision_kind
        OR NEW.immutable_locator_digest IS NOT OLD.immutable_locator_digest
        OR NEW.immutable_manifest_digest IS NOT OLD.immutable_manifest_digest
        OR NEW.observation_fingerprint IS NOT OLD.observation_fingerprint
        OR NEW.catalog_generation_id IS NOT OLD.catalog_generation_id
        OR NEW.observed_at IS NOT OLD.observed_at
        OR NEW.encrypted_source_locator IS NOT OLD.encrypted_source_locator
        OR NEW.target_mode IS NOT OLD.target_mode
        OR NEW.target_node_id IS NOT OLD.target_node_id
        OR NEW.target_root_id IS NOT OLD.target_root_id
        OR NEW.encrypted_target_root_locator IS NOT OLD.encrypted_target_root_locator
        OR NEW.encrypted_target_relative_path IS NOT OLD.encrypted_target_relative_path
        OR NEW.root_locator_digest IS NOT OLD.root_locator_digest
        OR NEW.path_digest IS NOT OLD.path_digest
        OR NEW.target_base_revision IS NOT OLD.target_base_revision
        OR NEW.credential_scope_revision IS NOT OLD.credential_scope_revision
        OR NEW.root_revision IS NOT OLD.root_revision
        OR NEW.filesystem_revision IS NOT OLD.filesystem_revision
        OR NEW.selection_digest IS NOT OLD.selection_digest
        OR NEW.binding_digest IS NOT OLD.binding_digest
        OR NEW.capability_revision IS NOT OLD.capability_revision
        OR NEW.conflict_policy IS NOT OLD.conflict_policy
        OR NEW.operation_set_digest IS NOT OLD.operation_set_digest
        OR NEW.delete_set_digest IS NOT OLD.delete_set_digest
        OR NEW.security_decision IS NOT OLD.security_decision
        OR NEW.security_decision_digest IS NOT OLD.security_decision_digest
        OR NEW.security_finding_set_digest IS NOT OLD.security_finding_set_digest
        OR NEW.security_policy_revision IS NOT OLD.security_policy_revision
        OR NEW.security_override_binding_digest IS NOT OLD.security_override_binding_digest
        OR NEW.encrypted_override_reason IS NOT OLD.encrypted_override_reason
        OR NEW.preflight_revision IS NOT OLD.preflight_revision
        OR NEW.preflight_expires_at IS NOT OLD.preflight_expires_at
        OR NEW.estimated_items IS NOT OLD.estimated_items
        OR NEW.estimated_bytes IS NOT OLD.estimated_bytes
        OR NEW.created_at IS NOT OLD.created_at
    )
BEGIN
    SELECT RAISE(ABORT, 'authorized recovery plan binding is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_preflights_immutable
BEFORE UPDATE ON backup_asset_recovery_preflights
BEGIN
    SELECT RAISE(ABORT, 'recovery preflight snapshot is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_jobs_authority_insert
BEFORE INSERT ON backup_asset_recovery_jobs
BEGIN
    SELECT CASE WHEN NOT EXISTS (
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
    ) THEN RAISE(ABORT, 'recovery job authority binding mismatch') END;
END;

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
BEGIN
    SELECT RAISE(ABORT, 'recovery job authority binding is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_checkpoints_authority_insert
BEFORE INSERT ON backup_asset_recovery_checkpoints
BEGIN
    SELECT CASE WHEN EXISTS (
        SELECT 1
        FROM backup_asset_recovery_checkpoints AS terminal
        WHERE terminal.job_id = NEW.job_id
          AND terminal.phase = 'operation_unresolved'
    ) THEN RAISE(ABORT, 'unresolved operation checkpoint is terminal') END;
    SELECT CASE WHEN NOT EXISTS (
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
    ) THEN RAISE(ABORT, 'recovery checkpoint authority binding mismatch') END;
    SELECT CASE WHEN NEW.phase = 'operation' AND NOT EXISTS (
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
          AND attempt.mutation_armed = 1
          AND attempt.lease_expires_at > NEW.created_at
          AND julianday(attempt.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
          AND node_lease.holder_kind = 'recovery_job'
          AND node_lease.owner_id = attempt.owner_id
          AND node_lease.fence = NEW.node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
          AND julianday(node_lease.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
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
    ) THEN RAISE(ABORT, 'operation checkpoint binding mismatch') END;
    SELECT CASE WHEN NEW.phase = 'operation_unresolved' AND NOT EXISTS (
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
          AND attempt.mutation_armed = 1
          AND attempt.lease_expires_at > NEW.created_at
          AND julianday(attempt.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
          AND node_lease.holder_kind = 'recovery_job'
          AND node_lease.owner_id = attempt.owner_id
          AND node_lease.fence = NEW.node_fence
          AND node_lease.state = 'active'
          AND node_lease.lease_expires_at > NEW.created_at
          AND julianday(node_lease.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
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
    ) THEN RAISE(ABORT, 'unresolved operation checkpoint binding mismatch') END;
    SELECT CASE WHEN NEW.phase = 'delete_authority_required' AND NOT EXISTS (
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
    ) THEN RAISE(ABORT, 'delete-authority checkpoint binding mismatch') END;
    SELECT CASE WHEN NEW.phase = 'delete_authority_consumed' AND NOT EXISTS (
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
    ) THEN RAISE(ABORT, 'consumed exact-mirror delete authority binding mismatch') END;
END;

CREATE TRIGGER trg_backup_asset_recovery_evidence_worker_insert
BEFORE INSERT ON backup_asset_recovery_evidence
WHEN NEW.kind = 'failure' AND NOT EXISTS (
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
      AND attempt.mutation_armed = 1
      AND attempt.lease_expires_at > NEW.created_at
      AND julianday(attempt.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
      AND source_lease.id = NEW.source_lease_id
      AND source_lease.holder_type = 'recovery_job'
      AND source_lease.owner_id = NEW.job_id
      AND source_lease.attempt_id = NEW.attempt_id
      AND source_lease.fence_token <> ''
      AND source_lease.status = 'active'
      AND source_lease.lease_expires_at > NEW.created_at
      AND source_lease.absolute_deadline > NEW.created_at
      AND julianday(source_lease.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
      AND julianday(source_lease.absolute_deadline) > julianday(CURRENT_TIMESTAMP)
      AND node_lease.id = NEW.node_lease_id
      AND node_lease.holder_kind = 'recovery_job'
      AND node_lease.owner_id = attempt.owner_id
      AND node_lease.fence = NEW.node_lease_fence
      AND node_lease.state = 'active'
      AND node_lease.lease_expires_at > NEW.created_at
      AND julianday(node_lease.lease_expires_at) > julianday(CURRENT_TIMESTAMP)
)
BEGIN
    SELECT RAISE(ABORT, 'recovery worker failure evidence binding mismatch');
END;

CREATE TRIGGER trg_backup_asset_recovery_checkpoints_immutable
BEFORE UPDATE ON backup_asset_recovery_checkpoints
BEGIN
    SELECT RAISE(ABORT, 'recovery checkpoint is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_checkpoints_consumed_delete
BEFORE DELETE ON backup_asset_recovery_checkpoints
WHEN OLD.phase = 'delete_authority_consumed'
BEGIN
    SELECT RAISE(ABORT, 'consumed recovery checkpoint cannot be deleted');
END;

CREATE TRIGGER trg_backup_asset_recovery_checkpoints_consumed_replay
BEFORE INSERT ON backup_asset_recovery_checkpoints
WHEN EXISTS (
    SELECT 1 FROM backup_asset_recovery_checkpoints
    WHERE id = NEW.id AND phase = 'delete_authority_consumed'
)
BEGIN
    SELECT RAISE(ABORT, 'consumed recovery checkpoint cannot be replaced');
END;

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
WHEN (OLD.workspace_marker_binding_digest <> ''
        AND NEW.workspace_marker_binding_digest IS NOT OLD.workspace_marker_binding_digest)
	OR (OLD.workspace_owner <> '' AND NEW.workspace_owner IS NOT OLD.workspace_owner)
	OR (OLD.workspace_fence <> 0 AND NEW.workspace_fence IS NOT OLD.workspace_fence)
    OR ((NEW.workspace_marker_validation_attempt_id IS NOT OLD.workspace_marker_validation_attempt_id
            OR NEW.workspace_marker_validation_attempt_fence IS NOT OLD.workspace_marker_validation_attempt_fence
            OR NEW.workspace_marker_validation_node_fence IS NOT OLD.workspace_marker_validation_node_fence)
        AND NOT (
            OLD.target_mode = 'isolated' AND NEW.target_mode = 'isolated'
            AND OLD.workspace_phase = 'reserved' AND NEW.workspace_phase = 'marker_created'
            AND OLD.workspace_marker_validation_attempt_id = ''
            AND OLD.workspace_marker_validation_attempt_fence = 0
            AND OLD.workspace_marker_validation_node_fence = 0
            AND length(NEW.workspace_marker_validation_attempt_id) = 32
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
                  AND attempt.mutation_armed = 1
            )
        ))
    OR (OLD.plaintext_deadline IS NOT NULL
        AND NEW.plaintext_deadline IS NOT OLD.plaintext_deadline)
    OR (NEW.workspace_phase IS NOT OLD.workspace_phase AND NOT (
        OLD.target_mode = 'isolated' AND NEW.target_mode = 'isolated' AND (
            (OLD.workspace_phase = 'none' AND NEW.workspace_phase = 'reserved')
            OR (OLD.workspace_phase = 'reserved' AND NEW.workspace_phase IN ('marker_created', 'cleanup_due'))
            OR (OLD.workspace_phase = 'marker_created' AND NEW.workspace_phase IN ('writing', 'cleanup_due'))
            OR (OLD.workspace_phase = 'writing' AND NEW.workspace_phase IN ('sealed', 'cleanup_due'))
            OR (OLD.workspace_phase = 'sealed' AND NEW.workspace_phase IN ('published', 'cleanup_due'))
            OR (OLD.workspace_phase = 'cleanup_due' AND NEW.workspace_phase = 'workspace_cleaned')
        )
    ))
    OR (NEW.workspace_phase = 'published' AND EXISTS (
        SELECT 1 FROM backup_asset_recovery_attempts
        WHERE job_id = OLD.id AND state IN ('claimed', 'running')
    ))
    OR EXISTS (
        SELECT 1 FROM backup_asset_recovery_result_sets
        WHERE job_id = OLD.id
    ) AND (
        NEW.state IS NOT OLD.state
        OR NEW.failure_category IS NOT OLD.failure_category
        OR NEW.transition_revision IS NOT OLD.transition_revision
        OR NEW.workspace_phase IS NOT OLD.workspace_phase
        OR NEW.encrypted_workspace_relative_locator IS NOT OLD.encrypted_workspace_relative_locator
        OR NEW.workspace_marker_binding_digest IS NOT OLD.workspace_marker_binding_digest
        OR NEW.workspace_owner IS NOT OLD.workspace_owner
        OR NEW.workspace_fence IS NOT OLD.workspace_fence
        OR NEW.workspace_marker_validation_attempt_id IS NOT OLD.workspace_marker_validation_attempt_id
        OR NEW.workspace_marker_validation_attempt_fence IS NOT OLD.workspace_marker_validation_attempt_fence
        OR NEW.workspace_marker_validation_node_fence IS NOT OLD.workspace_marker_validation_node_fence
        OR NEW.workspace_cleanup_phase IS NOT OLD.workspace_cleanup_phase
        OR NEW.workspace_cleanup_owner IS NOT OLD.workspace_cleanup_owner
        OR NEW.workspace_cleanup_lease_expires_at IS NOT OLD.workspace_cleanup_lease_expires_at
        OR NEW.workspace_cleanup_fence IS NOT OLD.workspace_cleanup_fence
        OR NEW.workspace_cleanup_node_lease_id IS NOT OLD.workspace_cleanup_node_lease_id
        OR NEW.workspace_cleanup_node_fence IS NOT OLD.workspace_cleanup_node_fence
        OR NEW.workspace_cleanup_attempt IS NOT OLD.workspace_cleanup_attempt
        OR NEW.plaintext_deadline IS NOT OLD.plaintext_deadline
        OR NEW.target_chain_revision IS NOT OLD.target_chain_revision
    )
BEGIN
    SELECT RAISE(ABORT, 'recovery job publication binding is immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_jobs_state_transition
BEFORE UPDATE OF state ON backup_asset_recovery_jobs
WHEN (NEW.state IS NOT OLD.state AND NOT (
        (OLD.state = 'queued' AND NEW.state IN ('running', 'cancel_requested'))
        OR (OLD.state = 'running' AND NEW.state IN ('verifying', 'cancel_requested', 'failed', 'needs_attention'))
        OR (OLD.state = 'verifying' AND NEW.state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'cancel_requested'))
        OR (OLD.state = 'cancel_requested' AND NEW.state IN ('canceled', 'needs_attention'))
    ))
    OR (NEW.state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'canceled') AND EXISTS (
        SELECT 1 FROM backup_asset_recovery_attempts
        WHERE job_id = OLD.id AND state IN ('claimed', 'running')
    ))
BEGIN
    SELECT RAISE(ABORT, 'recovery job state transition is invalid or has an active attempt');
END;

CREATE TRIGGER trg_backup_asset_recovery_attempts_publication_integrity
BEFORE INSERT ON backup_asset_recovery_attempts
WHEN NEW.state IN ('claimed', 'running') AND EXISTS (
    SELECT 1 FROM backup_asset_recovery_jobs AS job
    WHERE job.id = NEW.job_id
      AND (job.workspace_phase = 'published' OR EXISTS (
        SELECT 1 FROM backup_asset_recovery_result_sets WHERE job_id = job.id
      ))
)
BEGIN
    SELECT RAISE(ABORT, 'published recovery job cannot acquire an active attempt');
END;

CREATE TRIGGER trg_backup_asset_recovery_attempts_terminal_job_barrier
BEFORE INSERT ON backup_asset_recovery_attempts
WHEN NEW.state IN ('claimed', 'running') AND EXISTS (
    SELECT 1 FROM backup_asset_recovery_jobs
    WHERE id = NEW.job_id AND state IN ('succeeded', 'degraded', 'failed', 'needs_attention', 'canceled')
)
BEGIN
    SELECT RAISE(ABORT, 'terminal recovery job cannot acquire an active attempt');
END;

CREATE TRIGGER trg_backup_asset_recovery_result_sets_publish
BEFORE INSERT ON backup_asset_recovery_result_sets
BEGIN
    SELECT CASE WHEN NEW.state <> 'ready' OR NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_jobs
        WHERE id = NEW.job_id AND target_mode = 'isolated' AND workspace_phase = 'published'
          AND state IN ('succeeded', 'degraded')
          AND workspace_marker_binding_digest = NEW.marker_binding_digest
          AND plaintext_deadline = NEW.plaintext_deadline
          AND NOT EXISTS (
            SELECT 1 FROM backup_asset_recovery_attempts
            WHERE job_id = NEW.job_id AND state IN ('claimed', 'running')
          )
    ) THEN RAISE(ABORT, 'recovery result set requires published isolated terminal job') END;
END;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_deadline_integrity
BEFORE UPDATE OF marker_binding_digest, plaintext_deadline, hard_deadline
ON backup_asset_recovery_result_sets
WHEN NEW.marker_binding_digest IS NOT OLD.marker_binding_digest
    OR NEW.hard_deadline IS NOT OLD.hard_deadline
    OR (OLD.state = 'cleaned' AND NEW.plaintext_deadline IS NOT OLD.plaintext_deadline)
    OR NEW.plaintext_deadline < OLD.plaintext_deadline
    OR NEW.plaintext_deadline > OLD.hard_deadline
BEGIN
    SELECT RAISE(ABORT, 'recovery result publication marker and deadlines are immutable');
END;

CREATE TRIGGER trg_backup_asset_recovery_result_sets_state_transition
BEFORE UPDATE ON backup_asset_recovery_result_sets
WHEN (NEW.state IS NOT OLD.state AND NOT (
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
        AND (NEW.cleanup_owner IS NOT OLD.cleanup_owner
            OR NEW.cleanup_fence IS NOT OLD.cleanup_fence
            OR NEW.cleanup_attempt IS NOT OLD.cleanup_attempt
            OR NEW.node_lease_id IS NOT OLD.node_lease_id
            OR NEW.node_fence IS NOT OLD.node_fence)
        AND NOT (OLD.cleanup_lease_expires_at <= CURRENT_TIMESTAMP
            AND NEW.cleanup_fence > OLD.cleanup_fence
            AND NEW.cleanup_attempt > OLD.cleanup_attempt))
BEGIN
    SELECT RAISE(ABORT, 'recovery result set state transition requires the current or a fresh expired fence');
END;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_terminal_delete
BEFORE DELETE ON backup_asset_recovery_result_sets
WHEN OLD.state = 'cleaned'
BEGIN
    SELECT RAISE(ABORT, 'cleaned recovery result-set tombstone cannot be deleted');
END;
CREATE TRIGGER trg_backup_asset_recovery_result_sets_terminal_replay
BEFORE INSERT ON backup_asset_recovery_result_sets
WHEN EXISTS (
    SELECT 1 FROM backup_asset_recovery_result_sets
    WHERE state = 'cleaned' AND (id = NEW.id OR job_id = NEW.job_id)
)
BEGIN
    SELECT RAISE(ABORT, 'cleaned recovery result-set tombstone cannot be replaced');
END;
CREATE TRIGGER trg_backup_asset_recovery_results_publish
BEFORE INSERT ON backup_asset_recovery_results
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM backup_asset_recovery_result_sets
        WHERE id = NEW.result_set_id AND job_id = NEW.job_id AND state = 'ready'
    ) THEN RAISE(ABORT, 'recovery result requires ready matching result set') END;
END;
CREATE TRIGGER trg_backup_asset_recovery_results_classification_immutable
BEFORE UPDATE OF classification, classification_revision, classification_source_revision
ON backup_asset_recovery_results
WHEN NEW.classification IS NOT OLD.classification
    OR NEW.classification_revision IS NOT OLD.classification_revision
    OR NEW.classification_source_revision IS NOT OLD.classification_source_revision
BEGIN
    SELECT RAISE(ABORT, 'recovery result classification binding is immutable');
END;

-- Rebuild the Content grant and request pair together. SQLite cannot ALTER a
-- CHECK or add the exact RecoveryResult composite foreign key in place, and
-- dropping the parent before its child would cascade legacy request rows.
CREATE TEMP TABLE backup_asset_delivery_requests_000069_hold AS
SELECT * FROM backup_asset_delivery_requests;
DROP TABLE backup_asset_delivery_requests;

CREATE TABLE backup_asset_delivery_grants_new (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    delivery_id TEXT NOT NULL UNIQUE CHECK (length(delivery_id) = 32 AND delivery_id NOT GLOB '*[^0-9a-f]*'),
    resource_kind TEXT NOT NULL CHECK (resource_kind IN ('backup_asset', 'recovery_result')),
    recovery_point_id TEXT,
    catalog_generation_id TEXT,
    entry_id TEXT,
    recovery_job_id TEXT CHECK (recovery_job_id IS NULL
        OR (length(recovery_job_id) = 32 AND recovery_job_id NOT GLOB '*[^0-9a-f]*')),
    recovery_result_id TEXT CHECK (recovery_result_id IS NULL
        OR (length(recovery_result_id) = 32 AND recovery_result_id NOT GLOB '*[^0-9a-f]*')),
    owner_user_id INTEGER NOT NULL,
    session_jti TEXT NOT NULL CHECK (length(session_jti) = 32 AND session_jti NOT GLOB '*[^0-9a-f]*'),
    session_token_version INTEGER NOT NULL CHECK (session_token_version >= 0),
    session_role TEXT NOT NULL CHECK (session_role IN ('admin', 'operator')),
    session_expires_at DATETIME NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('preview', 'download')),
    method_policy TEXT NOT NULL CHECK (method_policy = 'get_head'),
    range_policy TEXT NOT NULL CHECK (range_policy IN ('none', 'single')),
    renderer TEXT NOT NULL CHECK (renderer IN ('escaped_text', 'safe_raster', 'same_origin_pdf', 'native_audio', 'native_video', 'metadata_hex', 'attachment')),
    profile TEXT NOT NULL CHECK (profile IN ('text_v1', 'raster_v1', 'pdf_v1', 'audio_v1', 'video_v1', 'hex_v1', 'original_v1')),
    classification TEXT NOT NULL CHECK (classification IN ('non_secret', 'secret', 'unknown')),
    classification_revision INTEGER NOT NULL CHECK (classification_revision > 0),
    classification_source_revision INTEGER NOT NULL CHECK (classification_source_revision > 0),
    step_up_action TEXT,
    step_up_proof_id TEXT,
    step_up_expires_at DATETIME,
    provider_kind TEXT NOT NULL CHECK (provider_kind IN ('restic', 'rsync', 'rclone')),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    entry_fingerprint TEXT NOT NULL DEFAULT '' CHECK (length(entry_fingerprint) <= 128),
    fingerprint_strength TEXT NOT NULL CHECK (fingerprint_strength IN ('strong', 'weak', 'none')),
    representation_etag TEXT NOT NULL CHECK (length(representation_etag) BETWEEN 1 AND 160),
    source_size INTEGER NOT NULL CHECK (source_size >= 0),
    source_modified_at DATETIME,
    detected_media_type TEXT NOT NULL CHECK (length(detected_media_type) BETWEEN 1 AND 128),
    representation_source_bytes INTEGER NOT NULL CHECK (representation_source_bytes >= 0 AND representation_source_bytes <= source_size),
    representation_size INTEGER NOT NULL CHECK (representation_size >= 0),
    representation_truncated INTEGER NOT NULL CHECK (representation_truncated IN (0, 1)),
    cookie_secret_hash TEXT NOT NULL CHECK (length(cookie_secret_hash) = 64 AND cookie_secret_hash NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('issued', 'active', 'draining', 'revoked', 'expired', 'closed')),
    revocation_reason TEXT NOT NULL DEFAULT '' CHECK (revocation_reason IN ('', 'logout', 'session_revoked', 'session_changed', 'permission_changed', 'ownership_changed', 'classification_changed', 'point_unavailable', 'source_changed', 'lease_lost', 'expired', 'budget_exhausted', 'feature_disabled', 'shutdown', 'process_restarted', 'audit_failed', 'request_failed', 'cache_invalid')),
    revoked_at DATETIME,
    lease_id TEXT NOT NULL UNIQUE CHECK (length(lease_id) = 32 AND lease_id NOT GLOB '*[^0-9a-f]*'),
    lease_attempt_id TEXT NOT NULL CHECK (length(lease_attempt_id) = 32 AND lease_attempt_id NOT GLOB '*[^0-9a-f]*'),
    lease_fence_token_hash TEXT NOT NULL CHECK (length(lease_fence_token_hash) = 64 AND lease_fence_token_hash NOT GLOB '*[^0-9a-f]*'),
    absolute_expires_at DATETIME NOT NULL,
    idle_expires_at DATETIME NOT NULL,
    idle_ttl_seconds INTEGER NOT NULL CHECK (idle_ttl_seconds > 0),
    last_activity_at DATETIME NOT NULL,
    max_bytes_per_request INTEGER NOT NULL CHECK (max_bytes_per_request > 0),
    max_cumulative_bytes INTEGER NOT NULL CHECK (max_cumulative_bytes >= max_bytes_per_request),
    max_requests INTEGER NOT NULL CHECK (max_requests > 0),
    max_in_flight INTEGER NOT NULL CHECK (max_in_flight > 0),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    delivered_bytes INTEGER NOT NULL DEFAULT 0 CHECK (delivered_bytes >= 0),
    request_count INTEGER NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    in_flight INTEGER NOT NULL DEFAULT 0 CHECK (in_flight >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    audit_state TEXT NOT NULL DEFAULT 'none' CHECK (audit_state IN ('none', 'pending', 'emitted', 'retry_wait', 'failed')),
    audit_range_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_range_count >= 0),
    audit_range_bytes INTEGER NOT NULL DEFAULT 0 CHECK (audit_range_bytes >= 0),
    audit_request_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_request_count >= 0),
    audit_success_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_success_count >= 0),
    audit_blocked_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_blocked_count >= 0),
    audit_failure_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_failure_count >= 0),
    audit_failure_code TEXT NOT NULL DEFAULT '' CHECK (audit_failure_code IN ('', 'audit_write_failed', 'audit_backlog_full', 'reconciliation_failed')),
    audit_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_attempt_count >= 0),
    audit_next_attempt_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (
        (resource_kind = 'backup_asset' AND recovery_point_id IS NOT NULL AND catalog_generation_id IS NOT NULL AND entry_id IS NOT NULL
            AND recovery_job_id IS NULL AND recovery_result_id IS NULL)
        OR (resource_kind = 'recovery_result' AND recovery_point_id IS NULL AND catalog_generation_id IS NULL AND entry_id IS NULL
            AND recovery_job_id IS NOT NULL AND recovery_result_id IS NOT NULL)
    ),
    CHECK (
        (renderer = 'escaped_text' AND profile = 'text_v1' AND range_policy = 'none')
        OR (renderer = 'safe_raster' AND profile = 'raster_v1')
        OR (renderer = 'same_origin_pdf' AND profile = 'pdf_v1')
        OR (renderer = 'native_audio' AND profile = 'audio_v1')
        OR (renderer = 'native_video' AND profile = 'video_v1')
        OR (renderer = 'metadata_hex' AND profile = 'hex_v1' AND range_policy = 'none')
        OR (renderer = 'attachment' AND profile = 'original_v1')
    ),
    CHECK (
        (renderer IN ('safe_raster', 'same_origin_pdf', 'native_audio', 'native_video', 'attachment')
            AND representation_source_bytes = source_size AND representation_size = source_size AND representation_truncated = 0)
        OR (renderer IN ('escaped_text', 'metadata_hex')
            AND ((representation_truncated = 0 AND representation_source_bytes = source_size)
                OR (representation_truncated = 1 AND representation_source_bytes < source_size)))
    ),
    CHECK (
        (resource_kind = 'backup_asset' AND action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.download' AND step_up_proof_id IS NOT NULL
            AND length(step_up_proof_id) = 32 AND step_up_proof_id NOT GLOB '*[^0-9a-f]*'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (resource_kind = 'backup_asset' AND action = 'preview' AND renderer <> 'attachment' AND classification = 'non_secret'
            AND step_up_action IS NULL AND step_up_proof_id IS NULL AND step_up_expires_at IS NULL)
        OR (resource_kind = 'backup_asset' AND action = 'preview' AND renderer <> 'attachment' AND classification IN ('secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.secret_reveal' AND step_up_proof_id IS NOT NULL
            AND length(step_up_proof_id) = 32 AND step_up_proof_id NOT GLOB '*[^0-9a-f]*'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (resource_kind = 'recovery_result' AND action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND classification IN ('non_secret', 'secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'recovery.result_download'
            AND step_up_proof_id IS NOT NULL AND length(step_up_proof_id) = 32
            AND step_up_proof_id NOT GLOB '*[^0-9a-f]*'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
    ),
    CHECK ((state IN ('issued', 'active', 'closed') AND revocation_reason = '' AND revoked_at IS NULL)
        OR (state IN ('draining', 'revoked', 'expired') AND revocation_reason <> '' AND revoked_at IS NOT NULL)),
    CHECK (last_activity_at <= idle_expires_at AND idle_expires_at <= absolute_expires_at AND absolute_expires_at <= session_expires_at),
    CHECK (delivered_bytes <= max_cumulative_bytes AND reserved_bytes <= max_cumulative_bytes - delivered_bytes),
    CHECK (request_count <= max_requests AND in_flight <= max_in_flight),
    CHECK (
        (audit_state = 'none' AND audit_range_count = 0 AND audit_range_bytes = 0 AND audit_request_count = 0
            AND audit_success_count = 0 AND audit_blocked_count = 0 AND audit_failure_count = 0
            AND audit_failure_code = '' AND audit_attempt_count = 0 AND audit_next_attempt_at IS NULL)
        OR (audit_state IN ('pending', 'emitted', 'retry_wait', 'failed') AND audit_request_count > 0
            AND audit_request_count <= request_count AND audit_range_count <= audit_request_count
            AND (audit_range_count > 0 OR audit_range_bytes = 0) AND audit_range_bytes <= delivered_bytes
            AND audit_success_count + audit_blocked_count + audit_failure_count = audit_request_count
            AND ((audit_state = 'pending' AND audit_failure_code = '' AND audit_attempt_count = 0 AND audit_next_attempt_at IS NULL)
                OR (audit_state = 'emitted' AND audit_failure_code = '' AND audit_next_attempt_at IS NULL)
                OR (audit_state IN ('retry_wait', 'failed') AND audit_failure_code <> '' AND audit_attempt_count > 0 AND audit_next_attempt_at IS NOT NULL)))
    ),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT,
    FOREIGN KEY (lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT,
    FOREIGN KEY (recovery_job_id) REFERENCES backup_asset_recovery_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (recovery_result_id, recovery_job_id)
        REFERENCES backup_asset_recovery_results(id, job_id) ON DELETE RESTRICT
);

INSERT INTO backup_asset_delivery_grants_new
SELECT * FROM backup_asset_delivery_grants;
DROP TABLE backup_asset_delivery_grants;
ALTER TABLE backup_asset_delivery_grants_new RENAME TO backup_asset_delivery_grants;
CREATE INDEX idx_backup_asset_delivery_grants_delivery_state ON backup_asset_delivery_grants(delivery_id, state);
CREATE INDEX idx_backup_asset_delivery_grants_session_state ON backup_asset_delivery_grants(owner_user_id, session_jti, state);
CREATE INDEX idx_backup_asset_delivery_grants_resource_state ON backup_asset_delivery_grants(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_delivery_grants_expiry ON backup_asset_delivery_grants(state, idle_expires_at, absolute_expires_at);
CREATE INDEX idx_backup_asset_delivery_grants_audit ON backup_asset_delivery_grants(audit_state, audit_next_attempt_at, updated_at);
CREATE INDEX idx_backup_asset_delivery_grants_recovery_result_state
    ON backup_asset_delivery_grants(recovery_job_id, recovery_result_id, state);

CREATE TRIGGER trg_backup_asset_recovery_content_authorization_insert
BEFORE INSERT ON backup_asset_delivery_grants
WHEN NEW.resource_kind = 'recovery_result'
BEGIN
    SELECT CASE WHEN NEW.session_role <> 'admin'
        OR NEW.action <> 'download'
        OR NEW.renderer <> 'attachment'
        OR NEW.profile <> 'original_v1'
        OR NEW.step_up_action IS NOT 'recovery.result_download'
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
        )
    THEN RAISE(ABORT, 'recovery result Content authorization binding mismatch') END;
END;

CREATE TRIGGER trg_backup_asset_recovery_content_authorization_update
BEFORE UPDATE OF resource_kind ON backup_asset_delivery_grants
WHEN OLD.resource_kind <> 'recovery_result' AND NEW.resource_kind = 'recovery_result'
BEGIN
    SELECT CASE WHEN NEW.session_role <> 'admin'
        OR NEW.action <> 'download'
        OR NEW.renderer <> 'attachment'
        OR NEW.profile <> 'original_v1'
        OR NEW.step_up_action IS NOT 'recovery.result_download'
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
        )
    THEN RAISE(ABORT, 'recovery result Content authorization binding mismatch') END;
END;

CREATE TRIGGER trg_backup_asset_recovery_content_binding_immutable
BEFORE UPDATE OF
    id, delivery_id, resource_kind, recovery_point_id, catalog_generation_id, entry_id,
    recovery_job_id, recovery_result_id, owner_user_id, session_jti,
    session_token_version, session_role, session_expires_at, action, method_policy,
    renderer, profile, classification, classification_revision,
    classification_source_revision, step_up_action, step_up_proof_id,
    step_up_expires_at, absolute_expires_at, created_at
ON backup_asset_delivery_grants
WHEN OLD.resource_kind = 'recovery_result' AND (
    NEW.id IS NOT OLD.id
    OR NEW.delivery_id IS NOT OLD.delivery_id
    OR NEW.resource_kind IS NOT OLD.resource_kind
    OR NEW.recovery_point_id IS NOT OLD.recovery_point_id
    OR NEW.catalog_generation_id IS NOT OLD.catalog_generation_id
    OR NEW.entry_id IS NOT OLD.entry_id
    OR NEW.recovery_job_id IS NOT OLD.recovery_job_id
    OR NEW.recovery_result_id IS NOT OLD.recovery_result_id
    OR NEW.owner_user_id IS NOT OLD.owner_user_id
    OR NEW.session_jti IS NOT OLD.session_jti
    OR NEW.session_token_version IS NOT OLD.session_token_version
    OR NEW.session_role IS NOT OLD.session_role
    OR NEW.session_expires_at IS NOT OLD.session_expires_at
    OR NEW.action IS NOT OLD.action
    OR NEW.method_policy IS NOT OLD.method_policy
    OR NEW.renderer IS NOT OLD.renderer
    OR NEW.profile IS NOT OLD.profile
    OR NEW.classification IS NOT OLD.classification
    OR NEW.classification_revision IS NOT OLD.classification_revision
    OR NEW.classification_source_revision IS NOT OLD.classification_source_revision
    OR NEW.step_up_action IS NOT OLD.step_up_action
    OR NEW.step_up_proof_id IS NOT OLD.step_up_proof_id
    OR NEW.step_up_expires_at IS NOT OLD.step_up_expires_at
    OR NEW.absolute_expires_at IS NOT OLD.absolute_expires_at
    OR NEW.created_at IS NOT OLD.created_at
)
BEGIN
    SELECT RAISE(ABORT, 'recovery result Content authorization binding is immutable');
END;

CREATE TABLE backup_asset_delivery_requests (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    grant_id TEXT NOT NULL CHECK (length(grant_id) = 32),
    method TEXT NOT NULL CHECK (method IN ('GET', 'HEAD')),
    range_kind TEXT NOT NULL CHECK (range_kind IN ('full', 'normal', 'open_ended', 'suffix')),
    range_start INTEGER,
    range_end_exclusive INTEGER,
    suffix_length INTEGER,
    state TEXT NOT NULL CHECK (state IN ('reserved', 'streaming', 'succeeded', 'blocked', 'canceled', 'failed', 'reconciled')),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (provider_bytes >= 0 AND provider_bytes <= reserved_bytes),
    response_bytes INTEGER NOT NULL DEFAULT 0 CHECK (response_bytes >= 0 AND response_bytes <= reserved_bytes),
    http_status INTEGER NOT NULL DEFAULT 0 CHECK (http_status = 0 OR (http_status BETWEEN 100 AND 599)),
    failure_code TEXT NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'invalid_range', 'range_not_allowed', 'if_range_full_forbidden', 'request_too_large', 'budget_exhausted', 'session_revoked', 'permission_changed', 'source_changed', 'lease_lost', 'feature_disabled', 'client_canceled', 'write_failed', 'source_failed', 'reconciled_crash', 'internal_failure')),
    started_at DATETIME NOT NULL,
    last_progress_at DATETIME NOT NULL,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK ((range_kind = 'full' AND range_start IS NULL AND range_end_exclusive IS NULL AND suffix_length IS NULL)
        OR (range_kind = 'normal' AND range_start IS NOT NULL AND range_start >= 0 AND range_end_exclusive IS NOT NULL AND range_end_exclusive > range_start AND suffix_length IS NULL)
        OR (range_kind = 'open_ended' AND range_start IS NOT NULL AND range_start >= 0 AND range_end_exclusive IS NULL AND suffix_length IS NULL)
        OR (range_kind = 'suffix' AND range_start IS NULL AND range_end_exclusive IS NULL AND suffix_length IS NOT NULL AND suffix_length > 0)),
    CHECK (method <> 'HEAD' OR reserved_bytes = 0),
    CHECK (last_progress_at >= started_at AND (finished_at IS NULL OR finished_at >= started_at)),
    CHECK ((state IN ('reserved', 'streaming') AND finished_at IS NULL AND http_status = 0 AND failure_code = '')
        OR (state = 'succeeded' AND finished_at IS NOT NULL AND http_status BETWEEN 200 AND 299 AND failure_code = '')
        OR (state IN ('blocked', 'canceled', 'failed', 'reconciled') AND finished_at IS NOT NULL AND http_status BETWEEN 100 AND 599 AND failure_code <> '')),
    FOREIGN KEY (grant_id) REFERENCES backup_asset_delivery_grants(id) ON DELETE CASCADE
);
INSERT INTO backup_asset_delivery_requests SELECT * FROM backup_asset_delivery_requests_000069_hold;
DROP TABLE backup_asset_delivery_requests_000069_hold;
CREATE INDEX idx_backup_asset_delivery_requests_grant_state ON backup_asset_delivery_requests(grant_id, state, started_at);
CREATE INDEX idx_backup_asset_delivery_requests_reconcile ON backup_asset_delivery_requests(state, last_progress_at);

-- golang-migrate marks the target version dirty before it executes a down
-- body. Reject a used 000069 downgrade at that durable metadata boundary so
-- its SetVersion transaction rolls back and leaves version 69 clean.
CREATE TRIGGER trg_backup_asset_recovery_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 69 AND (
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
)
BEGIN
    SELECT RAISE(ABORT, '000069 downgrade blocked: recovery, recovery content, or recovery lease state exists');
END;
