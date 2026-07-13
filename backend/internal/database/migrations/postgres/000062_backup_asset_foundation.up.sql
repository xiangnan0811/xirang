BEGIN;

ALTER TABLE tasks ADD COLUMN archived_at TIMESTAMPTZ;

CREATE TABLE backup_repositories (
    id VARCHAR(32) PRIMARY KEY,
    provider_kind VARCHAR(32) NOT NULL CHECK (provider_kind IN ('restic', 'rsync', 'rclone', 'command')),
    repository_identity TEXT,
    display_name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version_mode VARCHAR(32) NOT NULL CHECK (version_mode IN ('native_snapshot', 'hardlink_tree', 'full_copy_tree', 'versioned_prefix', 'native_object_versions', 'mutable_head')),
    status VARCHAR(32) NOT NULL CHECK (status IN ('connecting', 'online', 'degraded', 'offline', 'disconnected', 'purging', 'purge_blocked')),
    capability_revision INTEGER NOT NULL DEFAULT 1 CHECK (capability_revision > 0),
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    immutability_level VARCHAR(32) NOT NULL CHECK (immutability_level IN ('mutable', 'xirang_managed', 'backend_versioned', 'storage_worm')),
    last_seen_at TIMESTAMPTZ,
    last_reconciled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX idx_backup_repositories_provider_identity
    ON backup_repositories(provider_kind, repository_identity)
    WHERE repository_identity IS NOT NULL;
CREATE INDEX idx_backup_repositories_provider_kind ON backup_repositories(provider_kind);
CREATE INDEX idx_backup_repositories_status ON backup_repositories(status);

CREATE TABLE repository_access_bindings (
    id VARCHAR(32) PRIMARY KEY,
    repository_id VARCHAR(32) NOT NULL,
    binding_kind VARCHAR(32) NOT NULL,
    encrypted_config TEXT NOT NULL,
    config_fingerprint VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('active', 'revoked')),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (repository_id) REFERENCES backup_repositories(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_repository_access_bindings_active
    ON repository_access_bindings(repository_id)
    WHERE status = 'active';
CREATE INDEX idx_repository_access_bindings_repository_id ON repository_access_bindings(repository_id);

CREATE TABLE task_repository_links (
    id VARCHAR(32) PRIMARY KEY,
    task_id BIGINT,
    repository_id VARCHAR(32) NOT NULL,
    task_name_snapshot VARCHAR(255) NOT NULL DEFAULT '',
    node_id_snapshot BIGINT NOT NULL DEFAULT 0,
    node_name_snapshot VARCHAR(255) NOT NULL DEFAULT '',
    publication_mode VARCHAR(32) NOT NULL CHECK (publication_mode IN ('legacy_mutable', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions')),
    encrypted_legacy_locator TEXT NOT NULL DEFAULT '',
    linked_at TIMESTAMPTZ NOT NULL,
    unlinked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE SET NULL,
    FOREIGN KEY (repository_id) REFERENCES backup_repositories(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX idx_task_repository_links_active_task
    ON task_repository_links(task_id)
    WHERE task_id IS NOT NULL AND unlinked_at IS NULL;
CREATE INDEX idx_task_repository_links_repository_id ON task_repository_links(repository_id);

CREATE TABLE recovery_points (
    id VARCHAR(32) PRIMARY KEY,
    repository_id VARCHAR(32) NOT NULL,
    producing_task_id BIGINT,
    producing_task_run_id BIGINT,
    producing_task_name_snapshot VARCHAR(255) NOT NULL DEFAULT '',
    producing_node_id_snapshot BIGINT NOT NULL DEFAULT 0,
    producing_node_name_snapshot VARCHAR(255) NOT NULL DEFAULT '',
    lineage_json TEXT NOT NULL DEFAULT '{}',
    encrypted_provider_locator TEXT NOT NULL DEFAULT '',
    encrypted_rollback_locator TEXT NOT NULL DEFAULT '',
    semantics VARCHAR(32) NOT NULL CHECK (semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline', 'mutable_head')),
    state VARCHAR(32) NOT NULL CHECK (state IN ('observed', 'retired', 'preparing', 'verifying', 'committed', 'degraded', 'expiring', 'expired', 'failed', 'purge_blocked')),
    captured_at TIMESTAMPTZ,
    committed_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ,
    source_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    manifest_digest_algorithm VARCHAR(32) NOT NULL DEFAULT 'sha256',
    manifest_digest VARCHAR(128) NOT NULL DEFAULT '',
    entry_count BIGINT NOT NULL DEFAULT 0 CHECK (entry_count >= 0),
    logical_bytes BIGINT NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0),
    consistency_json TEXT NOT NULL DEFAULT '{}',
    fidelity_json TEXT NOT NULL DEFAULT '{}',
    capability_revision INTEGER NOT NULL DEFAULT 1 CHECK (capability_revision > 0),
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    immutability_level VARCHAR(32) NOT NULL CHECK (immutability_level IN ('mutable', 'xirang_managed', 'backend_versioned', 'storage_worm')),
    physical_availability VARCHAR(16) NOT NULL CHECK (physical_availability IN ('online', 'offline', 'missing', 'unknown')),
    hold_state VARCHAR(16) NOT NULL CHECK (hold_state IN ('none', 'active', 'released')),
    hold_until TIMESTAMPTZ,
    retention_until TIMESTAMPTZ,
    retirement_reason VARCHAR(16) CHECK (retirement_reason IS NULL OR retirement_reason IN ('cutover', 'withdrawn')),
    retired_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (repository_id) REFERENCES backup_repositories(id) ON DELETE RESTRICT,
    FOREIGN KEY (producing_task_id) REFERENCES tasks(id) ON DELETE SET NULL,
    FOREIGN KEY (producing_task_run_id) REFERENCES task_runs(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX idx_recovery_points_mutable_head
    ON recovery_points(repository_id)
    WHERE semantics = 'mutable_head';
CREATE INDEX idx_recovery_points_repository_state ON recovery_points(repository_id, state);
CREATE INDEX idx_recovery_points_producing_task_id ON recovery_points(producing_task_id);
CREATE INDEX idx_recovery_points_producing_task_run_id ON recovery_points(producing_task_run_id);
CREATE INDEX idx_recovery_points_retention_until ON recovery_points(retention_until);

CREATE TABLE recovery_point_manifests (
    id VARCHAR(32) PRIMARY KEY,
    recovery_point_id VARCHAR(32) NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    digest_algorithm VARCHAR(32) NOT NULL,
    digest VARCHAR(128) NOT NULL,
    generator VARCHAR(64) NOT NULL,
    generator_version VARCHAR(64) NOT NULL,
    completeness VARCHAR(16) NOT NULL CHECK (completeness IN ('complete', 'partial', 'unavailable')),
    entry_count BIGINT NOT NULL DEFAULT 0 CHECK (entry_count >= 0),
    logical_bytes BIGINT NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0),
    fidelity_json TEXT NOT NULL DEFAULT '{}',
    encrypted_commit_evidence TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE,
    UNIQUE (recovery_point_id, revision)
);

CREATE UNIQUE INDEX idx_recovery_point_manifests_active
    ON recovery_point_manifests(recovery_point_id)
    WHERE is_active = TRUE;
CREATE INDEX idx_recovery_point_manifests_recovery_point_id ON recovery_point_manifests(recovery_point_id);

CREATE TABLE catalog_generations (
    id VARCHAR(32) PRIMARY KEY,
    recovery_point_id VARCHAR(32) NOT NULL,
    manifest_id VARCHAR(32),
    generation INTEGER NOT NULL CHECK (generation > 0),
    state VARCHAR(16) NOT NULL CHECK (state IN ('building', 'complete', 'partial', 'failed', 'superseded')),
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    source_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    expected_entry_count BIGINT NOT NULL DEFAULT 0 CHECK (expected_entry_count >= 0),
    written_entry_count BIGINT NOT NULL DEFAULT 0 CHECK (written_entry_count >= 0),
    expected_digest VARCHAR(128) NOT NULL DEFAULT '',
    written_digest VARCHAR(128) NOT NULL DEFAULT '',
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    correlation_id VARCHAR(64) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE,
    FOREIGN KEY (manifest_id) REFERENCES recovery_point_manifests(id) ON DELETE SET NULL,
    UNIQUE (recovery_point_id, generation)
);

CREATE UNIQUE INDEX idx_catalog_generations_active
    ON catalog_generations(recovery_point_id)
    WHERE is_active = TRUE;
CREATE INDEX idx_catalog_generations_recovery_point_id ON catalog_generations(recovery_point_id);
CREATE INDEX idx_catalog_generations_manifest_id ON catalog_generations(manifest_id);

CREATE TABLE catalog_entries (
    generation_id VARCHAR(32) NOT NULL,
    entry_id VARCHAR(64) NOT NULL,
    recovery_point_id VARCHAR(32) NOT NULL,
    parent_entry_id VARCHAR(64),
    normalized_path TEXT NOT NULL,
    name TEXT NOT NULL,
    entry_type VARCHAR(16) NOT NULL CHECK (entry_type IN ('file', 'directory', 'symlink', 'hardlink', 'special', 'unknown')),
    size BIGINT NOT NULL DEFAULT 0 CHECK (size >= 0),
    modified_at TIMESTAMPTZ,
    mode VARCHAR(32) NOT NULL DEFAULT '',
    owner VARCHAR(255) NOT NULL DEFAULT '',
    mime_type VARCHAR(255) NOT NULL DEFAULT '',
    fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    fingerprint_strength VARCHAR(32) NOT NULL DEFAULT '',
    encrypted_provider_locator TEXT NOT NULL DEFAULT '',
    security_state VARCHAR(32) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (generation_id, entry_id),
    FOREIGN KEY (generation_id) REFERENCES catalog_generations(id) ON DELETE CASCADE,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE
);

CREATE INDEX idx_catalog_entries_listing
    ON catalog_entries(recovery_point_id, generation_id, parent_entry_id, name, entry_id);
CREATE INDEX idx_catalog_entries_entry_id ON catalog_entries(entry_id);

CREATE TABLE wrapped_domain_keys (
    id VARCHAR(32) PRIMARY KEY,
    domain VARCHAR(32) NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership')),
    version INTEGER NOT NULL CHECK (version > 0),
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'verify_only', 'retired', 'lost')),
    wrapped_key TEXT NOT NULL,
    wrap_algorithm VARCHAR(32) NOT NULL,
    wrapping_key_fingerprint VARCHAR(64) NOT NULL,
    activated_at TIMESTAMPTZ NOT NULL,
    verify_until TIMESTAMPTZ,
    lost_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (domain, version)
);

CREATE UNIQUE INDEX idx_wrapped_domain_keys_active
    ON wrapped_domain_keys(domain)
    WHERE state = 'active';
CREATE INDEX idx_wrapped_domain_keys_domain_state ON wrapped_domain_keys(domain, state);

CREATE TABLE recovery_point_leases (
    id VARCHAR(32) PRIMARY KEY,
    recovery_point_id VARCHAR(32) NOT NULL,
    holder_type VARCHAR(32) NOT NULL CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job')),
    owner_id VARCHAR(64) NOT NULL,
    attempt_id VARCHAR(32) NOT NULL,
    fence_token VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('active', 'released', 'expired')),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    absolute_deadline TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_recovery_point_leases_active_owner_slot
    ON recovery_point_leases(recovery_point_id, holder_type, owner_id)
    WHERE status = 'active';
CREATE INDEX idx_recovery_point_leases_recovery_status_expiry
    ON recovery_point_leases(recovery_point_id, status, lease_expires_at);
CREATE INDEX idx_recovery_point_leases_absolute_deadline ON recovery_point_leases(absolute_deadline);

CREATE TABLE backup_asset_audit_checkpoints (
    segment_no BIGINT PRIMARY KEY,
    status VARCHAR(16) NOT NULL CHECK (status IN ('open', 'closed', 'details_purged')),
    previous_checkpoint_hash VARCHAR(64) NOT NULL DEFAULT '',
    first_entry_hash VARCHAR(64) NOT NULL DEFAULT '',
    last_entry_hash VARCHAR(64) NOT NULL DEFAULT '',
    entry_count BIGINT NOT NULL DEFAULT 0 CHECK (entry_count >= 0),
    opened_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    details_purged_at TIMESTAMPTZ,
    checkpoint_hash VARCHAR(64) NOT NULL DEFAULT ''
);

CREATE INDEX idx_backup_asset_audit_checkpoints_status
    ON backup_asset_audit_checkpoints(status);

CREATE TABLE backup_asset_audit_events (
    id BIGSERIAL PRIMARY KEY,
    segment_no BIGINT NOT NULL,
    segment_sequence BIGINT NOT NULL CHECK (segment_sequence > 0),
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    actor_username VARCHAR(64) NOT NULL DEFAULT '',
    actor_role VARCHAR(32) NOT NULL DEFAULT '',
    action VARCHAR(64) NOT NULL,
    outcome VARCHAR(16) NOT NULL CHECK (outcome IN ('success', 'failure', 'blocked')),
    repository_id VARCHAR(32) NOT NULL DEFAULT '',
    recovery_point_id VARCHAR(32) NOT NULL DEFAULT '',
    entry_id VARCHAR(64) NOT NULL DEFAULT '',
    task_id BIGINT,
    task_run_id BIGINT,
    recovery_job_id VARCHAR(32) NOT NULL DEFAULT '',
    export_job_id VARCHAR(32) NOT NULL DEFAULT '',
    item_count BIGINT NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    byte_count BIGINT NOT NULL DEFAULT 0 CHECK (byte_count >= 0),
    range_count BIGINT NOT NULL DEFAULT 0 CHECK (range_count >= 0),
    range_bytes BIGINT NOT NULL DEFAULT 0 CHECK (range_bytes >= 0),
    fingerprint_key_version INTEGER,
    path_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    query_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    step_up_action VARCHAR(64) NOT NULL DEFAULT '',
    step_up_proof_id VARCHAR(64) NOT NULL DEFAULT '',
    grant_id VARCHAR(32) NOT NULL DEFAULT '',
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    fields_json TEXT NOT NULL DEFAULT '{}',
    prev_hash VARCHAR(64) NOT NULL,
    entry_hash VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (segment_no) REFERENCES backup_asset_audit_checkpoints(segment_no) ON DELETE CASCADE,
    UNIQUE (segment_no, segment_sequence)
);

CREATE INDEX idx_backup_asset_audit_events_action_created_at
    ON backup_asset_audit_events(action, created_at);
CREATE INDEX idx_backup_asset_audit_events_repository_created_at
    ON backup_asset_audit_events(repository_id, created_at);
CREATE INDEX idx_backup_asset_audit_events_recovery_point_created_at
    ON backup_asset_audit_events(recovery_point_id, created_at);

COMMIT;
