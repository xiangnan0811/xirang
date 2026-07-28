CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store', 'export_store')),
    version INTEGER NOT NULL CHECK (version > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'verify_only', 'retired', 'lost')),
    wrapped_key TEXT NOT NULL,
    wrap_algorithm TEXT NOT NULL,
    wrapping_key_fingerprint TEXT NOT NULL,
    activated_at DATETIME NOT NULL,
    verify_until DATETIME,
    lost_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (domain, version)
);

INSERT INTO wrapped_domain_keys_new
    (id, domain, version, state, wrapped_key, wrap_algorithm,
     wrapping_key_fingerprint, activated_at, verify_until, lost_at,
     created_at, updated_at)
SELECT id, domain, version, state, wrapped_key, wrap_algorithm,
       wrapping_key_fingerprint, activated_at, verify_until, lost_at,
       created_at, updated_at
FROM wrapped_domain_keys;

DROP TABLE wrapped_domain_keys;
ALTER TABLE wrapped_domain_keys_new RENAME TO wrapped_domain_keys;
CREATE UNIQUE INDEX idx_wrapped_domain_keys_active
    ON wrapped_domain_keys(domain) WHERE state = 'active';
CREATE INDEX idx_wrapped_domain_keys_domain_state
    ON wrapped_domain_keys(domain, state);

CREATE TABLE backup_asset_export_jobs (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    owner_user_id INTEGER NOT NULL,
    lifecycle_enqueue_sequence INTEGER NOT NULL DEFAULT 1 CHECK (lifecycle_enqueue_sequence > 0),
    selection_digest TEXT NOT NULL CHECK (length(selection_digest) = 64 AND selection_digest NOT GLOB '*[^0-9a-f]*'),
    selection_schema_version INTEGER NOT NULL CHECK (selection_schema_version = 1),
    archive_format TEXT NOT NULL CHECK (archive_format IN ('zip', 'tar')),
    archive_profile TEXT NOT NULL CHECK (
        (archive_format = 'zip' AND archive_profile = 'zip_deflate_v1')
        OR
        (archive_format = 'tar' AND archive_profile IN ('tar_none_v1', 'tar_gzip_v1'))
    ),
    limits_schema_version INTEGER NOT NULL CHECK (limits_schema_version = 1),
    chunk_bytes INTEGER NOT NULL CHECK (chunk_bytes > 0),
    max_items INTEGER NOT NULL CHECK (max_items > 0),
    max_source_points INTEGER NOT NULL CHECK (max_source_points > 0),
    max_item_bytes INTEGER NOT NULL CHECK (max_item_bytes > 0),
    max_logical_bytes INTEGER NOT NULL CHECK (max_logical_bytes > 0),
    max_provider_bytes INTEGER NOT NULL CHECK (max_provider_bytes > 0),
    max_ciphertext_bytes INTEGER NOT NULL CHECK (max_ciphertext_bytes > 0),
    max_open_readers INTEGER NOT NULL CHECK (max_open_readers > 0),
    max_duration_seconds INTEGER NOT NULL CHECK (max_duration_seconds > 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    retry_base_seconds INTEGER NOT NULL CHECK (retry_base_seconds > 0),
    retry_max_delay_seconds INTEGER NOT NULL CHECK (retry_max_delay_seconds >= retry_base_seconds),
    lease_ttl_seconds INTEGER NOT NULL CHECK (lease_ttl_seconds > 0),
    lease_renew_margin_seconds INTEGER NOT NULL CHECK (lease_renew_margin_seconds > 0 AND lease_renew_margin_seconds < lease_ttl_seconds),
    ready_ttl_seconds INTEGER NOT NULL CHECK (ready_ttl_seconds > 0),
    execution_state TEXT NOT NULL CHECK (execution_state IN ('queued', 'running', 'retry_wait', 'sealing', 'ready', 'cancel_requested', 'failed', 'source_expired', 'canceled', 'expiring', 'expired')),
    result_kind TEXT NOT NULL DEFAULT '' CHECK (result_kind IN ('', 'complete', 'partial')),
    cleanup_state TEXT NOT NULL CHECK (cleanup_state IN ('none', 'revoking', 'purging', 'purged', 'purge_failed')),
    current_attempt_id TEXT CHECK (current_attempt_id IS NULL OR length(current_attempt_id) = 32),
    current_fence_revision INTEGER NOT NULL DEFAULT 0 CHECK (current_fence_revision >= 0),
    absolute_deadline DATETIME NOT NULL,
    ready_at DATETIME,
    expires_at DATETIME,
    item_count INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    packed_count INTEGER NOT NULL DEFAULT 0 CHECK (packed_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    logical_bytes INTEGER NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0),
    provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (provider_bytes >= 0),
    artifact_bytes INTEGER NOT NULL DEFAULT 0 CHECK (artifact_bytes >= 0),
    error_category TEXT NOT NULL DEFAULT '' CHECK (length(error_category) <= 64),
    transition_revision INTEGER NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (packed_count + skipped_count + failed_count <= item_count),
    CHECK (max_source_points <= max_items),
    CHECK (max_item_bytes <= max_logical_bytes),
    CHECK (max_provider_bytes >= max_logical_bytes),
    CHECK (max_ciphertext_bytes >= max_logical_bytes + max_items * 1024 + 67108864),
    CHECK (lease_renew_margin_seconds * 2 < lease_ttl_seconds),
    CHECK (created_at <= updated_at),
    CHECK (execution_state NOT IN ('ready', 'expiring', 'expired') OR (ready_at IS NOT NULL AND expires_at IS NOT NULL)),
    CHECK (execution_state NOT IN ('ready', 'expiring', 'expired') OR (result_kind IN ('complete', 'partial') AND packed_count > 0)),
    CHECK (ready_at IS NULL OR (ready_at >= created_at AND expires_at IS NOT NULL AND ready_at < expires_at)),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_export_jobs_owner_state
    ON backup_asset_export_jobs(owner_user_id, execution_state, updated_at);
CREATE INDEX idx_backup_asset_export_jobs_claim
    ON backup_asset_export_jobs(execution_state, absolute_deadline, updated_at);
CREATE UNIQUE INDEX idx_backup_asset_export_jobs_lifecycle_enqueue_sequence
    ON backup_asset_export_jobs(lifecycle_enqueue_sequence);

CREATE TABLE backup_asset_export_keys (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    state TEXT NOT NULL CHECK (state IN ('active', 'destroyed', 'lost')),
    wrapped_dek BLOB NOT NULL,
    envelope_nonce BLOB NOT NULL,
    kek_version INTEGER NOT NULL CHECK (kek_version > 0),
    wrap_algorithm TEXT NOT NULL CHECK (length(wrap_algorithm) BETWEEN 1 AND 32),
    key_revision INTEGER NOT NULL DEFAULT 1 CHECK (key_revision > 0),
    created_at DATETIME NOT NULL,
    rewrapped_at DATETIME,
    destroyed_at DATETIME,
    CHECK (
        (state = 'active' AND length(wrapped_dek) > 0 AND length(envelope_nonce) = 12 AND destroyed_at IS NULL)
        OR
        (state IN ('destroyed', 'lost') AND length(wrapped_dek) = 0 AND length(envelope_nonce) = 0 AND destroyed_at IS NOT NULL)
    ),
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_backup_asset_export_keys_job
    ON backup_asset_export_keys(job_id);

CREATE TABLE backup_asset_export_items (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) = 64),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    entry_fingerprint TEXT NOT NULL CHECK (length(entry_fingerprint) BETWEEN 1 AND 128),
    fingerprint_strength TEXT NOT NULL CHECK (fingerprint_strength IN ('strong', 'weak', 'none')),
    provider_capability_revision INTEGER NOT NULL CHECK (provider_capability_revision > 0),
    entry_type TEXT NOT NULL CHECK (entry_type IN ('file', 'directory', 'symlink', 'hardlink', 'special', 'unknown')),
    logical_size INTEGER NOT NULL CHECK (logical_size >= 0),
    media_type TEXT NOT NULL DEFAULT '' CHECK (length(media_type) <= 255),
    retention_until DATETIME,
    selection_root_ordinal INTEGER NOT NULL CHECK (selection_root_ordinal >= 0),
    path_nonce BLOB NOT NULL,
    path_ciphertext BLOB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'read', 'packed', 'skipped', 'failed')),
    current_attempt_id TEXT CHECK (current_attempt_id IS NULL OR length(current_attempt_id) = 32),
    logical_bytes INTEGER NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0),
    provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (provider_bytes >= 0),
    error_category TEXT NOT NULL DEFAULT '' CHECK (length(error_category) <= 64),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (job_id, ordinal),
    UNIQUE (job_id, recovery_point_id, entry_id),
    CHECK ((length(path_nonce) = 12 AND length(path_ciphertext) > 0) OR
           (length(path_nonce) = 0 AND length(path_ciphertext) = 0)),
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE
);
CREATE INDEX idx_backup_asset_export_items_job_state
    ON backup_asset_export_items(job_id, state, ordinal);

CREATE TABLE backup_asset_export_attempts (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    worker_owner TEXT NOT NULL CHECK (length(worker_owner) BETWEEN 1 AND 128),
    state TEXT NOT NULL CHECK (state IN ('active', 'sealing', 'sealed', 'failed', 'canceled', 'superseded')),
    fence_token BLOB NOT NULL CHECK (length(fence_token) = 32),
    fence_digest TEXT NOT NULL CHECK (length(fence_digest) = 64 AND fence_digest NOT GLOB '*[^0-9a-f]*'),
    nonce_prefix BLOB NOT NULL CHECK (length(nonce_prefix) = 8),
    lease_expires_at DATETIME NOT NULL,
    checkpoint_ordinal INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_ordinal >= 0),
    checkpoint_item_count INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_item_count >= 0),
    checkpoint_logical_bytes INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_logical_bytes >= 0),
    checkpoint_provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_provider_bytes >= 0),
    staging_locator TEXT NOT NULL DEFAULT '' CHECK (length(staging_locator) <= 128 AND staging_locator NOT GLOB '*[/\\]*'),
    failure_category TEXT NOT NULL DEFAULT '' CHECK (length(failure_category) <= 64),
    is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
    started_at DATETIME NOT NULL,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (job_id, attempt_number),
    UNIQUE (nonce_prefix),
    CHECK ((is_current = 1 AND state IN ('active', 'sealing')) OR is_current = 0),
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_backup_asset_export_attempts_current
    ON backup_asset_export_attempts(job_id) WHERE is_current = 1;
CREATE UNIQUE INDEX idx_backup_asset_export_attempts_fence
    ON backup_asset_export_attempts(job_id, fence_digest);

CREATE TABLE backup_asset_export_item_attempts (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    item_id TEXT NOT NULL CHECK (length(item_id) = 32),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32),
    state TEXT NOT NULL CHECK (state IN ('pending', 'read', 'packed', 'skipped', 'failed')),
    spool_digest TEXT NOT NULL DEFAULT '' CHECK (length(spool_digest) IN (0, 64)),
    spool_size INTEGER NOT NULL DEFAULT 0 CHECK (spool_size >= 0),
    spool_locator TEXT NOT NULL DEFAULT '' CHECK (length(spool_locator) <= 128 AND spool_locator NOT GLOB '*[/\\]*'),
    logical_bytes INTEGER NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0),
    provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (provider_bytes >= 0),
    error_category TEXT NOT NULL DEFAULT '' CHECK (length(error_category) <= 64),
    started_at DATETIME NOT NULL,
    read_at DATETIME,
    packed_at DATETIME,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    UNIQUE (attempt_id, item_id),
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (item_id) REFERENCES backup_asset_export_items(id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_export_attempts(id) ON DELETE CASCADE
);

CREATE TABLE backup_asset_export_source_leases (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    lease_id TEXT NOT NULL CHECK (length(lease_id) = 32),
    lease_attempt_id TEXT NOT NULL CHECK (length(lease_attempt_id) = 32),
    fence_hash TEXT NOT NULL CHECK (length(fence_hash) = 64 AND fence_hash NOT GLOB '*[^0-9a-f]*'),
    absolute_deadline DATETIME NOT NULL,
    retention_until DATETIME,
    state TEXT NOT NULL CHECK (state IN ('active', 'released', 'lost', 'expired')),
    acquired_at DATETIME NOT NULL,
    renewed_at DATETIME NOT NULL,
    released_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (job_id, recovery_point_id),
    UNIQUE (lease_id),
    CHECK (acquired_at <= renewed_at AND renewed_at <= absolute_deadline),
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE
);

CREATE TABLE backup_asset_export_artifacts (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32),
    job_key_id TEXT NOT NULL CHECK (length(job_key_id) = 32),
    state TEXT NOT NULL CHECK (state IN ('staged', 'sealed', 'revoked', 'purging', 'purged', 'purge_failed')),
    locator TEXT NOT NULL UNIQUE CHECK (length(locator) BETWEEN 1 AND 128 AND locator NOT GLOB '*[/\\]*'),
    cipher_version INTEGER NOT NULL CHECK (cipher_version > 0),
    chunk_bytes INTEGER NOT NULL CHECK (chunk_bytes > 0),
    format_version INTEGER NOT NULL CHECK (format_version > 0),
    nonce_prefix BLOB NOT NULL CHECK (length(nonce_prefix) = 8),
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    plaintext_digest TEXT NOT NULL DEFAULT '' CHECK (length(plaintext_digest) IN (0, 64)),
    archive_digest TEXT NOT NULL DEFAULT '' CHECK (length(archive_digest) IN (0, 64)),
    ciphertext_digest TEXT NOT NULL DEFAULT '' CHECK (length(ciphertext_digest) IN (0, 64)),
    plaintext_size INTEGER NOT NULL DEFAULT 0 CHECK (plaintext_size >= 0),
    ciphertext_size INTEGER NOT NULL DEFAULT 0 CHECK (ciphertext_size >= 0),
    sealed_at DATETIME,
    expires_at DATETIME,
    purged_at DATETIME,
    purge_error TEXT NOT NULL DEFAULT '' CHECK (length(purge_error) <= 64),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (job_id),
    CHECK (expires_at IS NULL OR (sealed_at IS NOT NULL AND sealed_at <= expires_at)),
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_export_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (job_key_id) REFERENCES backup_asset_export_keys(id) ON DELETE RESTRICT
);

CREATE TABLE backup_asset_export_idempotency (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    owner_user_id INTEGER NOT NULL,
    endpoint TEXT NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 64),
    key_digest TEXT NOT NULL CHECK (length(key_digest) = 64 AND key_digest NOT GLOB '*[^0-9a-f]*'),
    request_intent_digest TEXT NOT NULL CHECK (length(request_intent_digest) = 64 AND request_intent_digest NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('creating', 'committed')),
    result_job_id TEXT CHECK (result_job_id IS NULL OR length(result_job_id) = 32),
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (owner_user_id, endpoint, key_digest),
    CHECK ((state = 'creating' AND result_job_id IS NULL) OR (state = 'committed' AND result_job_id IS NOT NULL)),
    CHECK (created_at <= expires_at),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT
);

CREATE TABLE backup_asset_export_quota_buckets (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    scope TEXT NOT NULL CHECK (scope IN ('global', 'user')),
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 64),
    transition_revision INTEGER NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    active_jobs INTEGER NOT NULL DEFAULT 0 CHECK (active_jobs >= 0),
    active_workers INTEGER NOT NULL DEFAULT 0 CHECK (active_workers >= 0),
    active_readers INTEGER NOT NULL DEFAULT 0 CHECK (active_readers >= 0),
    reserved_store_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_store_bytes >= 0),
    used_store_bytes INTEGER NOT NULL DEFAULT 0 CHECK (used_store_bytes >= 0),
    lifecycle_next_sequence INTEGER NOT NULL DEFAULT 1 CHECK (lifecycle_next_sequence > 0),
    lifecycle_sweep_cursor INTEGER NOT NULL DEFAULT 0 CHECK (lifecycle_sweep_cursor >= 0),
    lifecycle_sweep_high_water INTEGER NOT NULL DEFAULT 0 CHECK (lifecycle_sweep_high_water >= 0),
    lifecycle_sweep_revision INTEGER NOT NULL DEFAULT 0 CHECK (lifecycle_sweep_revision >= 0),
    lifecycle_sweep_lease_expires_at DATETIME,
    reader_next_sequence INTEGER NOT NULL DEFAULT 1 CHECK (reader_next_sequence > 0),
    reader_sweep_cursor INTEGER NOT NULL DEFAULT 0 CHECK (reader_sweep_cursor >= 0),
    reader_sweep_high_water INTEGER NOT NULL DEFAULT 0 CHECK (reader_sweep_high_water >= 0),
    reader_sweep_revision INTEGER NOT NULL DEFAULT 0 CHECK (reader_sweep_revision >= 0),
    reader_sweep_lease_expires_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (scope, subject),
    CHECK (
        (scope = 'global' AND subject = 'global'
            AND lifecycle_sweep_cursor <= lifecycle_sweep_high_water
            AND lifecycle_sweep_high_water < lifecycle_next_sequence
            AND (
                (lifecycle_sweep_revision = 0
                    AND lifecycle_sweep_cursor = 0
                    AND lifecycle_sweep_high_water = 0
                    AND lifecycle_sweep_lease_expires_at IS NULL)
                OR
                (lifecycle_sweep_revision > 0 AND lifecycle_sweep_high_water > 0)
            ))
        OR
        (scope = 'user'
            AND lifecycle_next_sequence = 1
            AND lifecycle_sweep_cursor = 0
            AND lifecycle_sweep_high_water = 0
            AND lifecycle_sweep_revision = 0
            AND lifecycle_sweep_lease_expires_at IS NULL)
    ),
    CHECK (
        (scope = 'global' AND subject = 'global'
            AND reader_sweep_cursor <= reader_sweep_high_water
            AND reader_sweep_high_water < reader_next_sequence
            AND (
                (reader_sweep_revision = 0
                    AND reader_sweep_cursor = 0
                    AND reader_sweep_high_water = 0
                    AND reader_sweep_lease_expires_at IS NULL)
                OR
                (reader_sweep_revision > 0 AND reader_sweep_high_water > 0)
            ))
        OR
        (scope = 'user'
            AND reader_next_sequence = 1
            AND reader_sweep_cursor = 0
            AND reader_sweep_high_water = 0
            AND reader_sweep_revision = 0
            AND reader_sweep_lease_expires_at IS NULL)
    )
);

CREATE TABLE backup_asset_export_reservations (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    bucket_id TEXT NOT NULL CHECK (length(bucket_id) = 32),
    job_id TEXT CHECK (job_id IS NULL OR length(job_id) = 32),
    attempt_id TEXT CHECK (attempt_id IS NULL OR length(attempt_id) = 32),
    kind TEXT NOT NULL CHECK (kind IN ('job', 'worker', 'reader', 'store')),
    reader_enqueue_sequence INTEGER NOT NULL DEFAULT 0 CHECK (reader_enqueue_sequence >= 0),
    reserved_slots INTEGER NOT NULL DEFAULT 0 CHECK (reserved_slots >= 0),
    reserved_logical_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_logical_bytes >= 0),
    reserved_provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_provider_bytes >= 0),
    reserved_cipher_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_cipher_bytes >= 0),
    reserved_store_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_store_bytes >= 0),
    lease_owner TEXT NOT NULL CHECK (length(lease_owner) BETWEEN 1 AND 128),
    lease_expires_at DATETIME NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'purge_pending', 'released', 'expired')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    released_at DATETIME,
    CHECK (
        (kind = 'reader' AND reader_enqueue_sequence > 0)
        OR (kind <> 'reader' AND reader_enqueue_sequence = 0)
    ),
    FOREIGN KEY (bucket_id) REFERENCES backup_asset_export_quota_buckets(id) ON DELETE RESTRICT,
    FOREIGN KEY (job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_export_attempts(id) ON DELETE CASCADE
);
CREATE INDEX idx_backup_asset_export_reservations_active
    ON backup_asset_export_reservations(bucket_id, kind, state, lease_expires_at);
CREATE UNIQUE INDEX idx_backup_asset_export_reservations_live_slot
    ON backup_asset_export_reservations(bucket_id, kind, lease_owner)
    WHERE state IN ('active', 'purge_pending');
CREATE UNIQUE INDEX idx_backup_asset_export_reservations_reader_enqueue_sequence
    ON backup_asset_export_reservations(bucket_id, reader_enqueue_sequence)
    WHERE kind = 'reader';
CREATE INDEX idx_backup_asset_export_reservations_reader_sweep
    ON backup_asset_export_reservations(bucket_id, reader_enqueue_sequence, lease_expires_at)
    WHERE kind = 'reader' AND state = 'active';

CREATE TABLE backup_asset_archive_member_requests (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    owner_user_id INTEGER NOT NULL,
    endpoint TEXT NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 64),
    key_digest TEXT NOT NULL CHECK (length(key_digest) = 64 AND key_digest NOT GLOB '*[^0-9a-f]*'),
    request_intent_digest TEXT NOT NULL CHECK (length(request_intent_digest) = 64 AND request_intent_digest NOT GLOB '*[^0-9a-f]*'),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) = 64),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    entry_fingerprint TEXT NOT NULL CHECK (length(entry_fingerprint) BETWEEN 1 AND 128),
    index_artifact_id TEXT NOT NULL CHECK (length(index_artifact_id) = 32),
    index_revision TEXT NOT NULL CHECK (length(index_revision) = 64),
    member_chain_digest TEXT NOT NULL CHECK (length(member_chain_digest) = 64 AND member_chain_digest NOT GLOB '*[^0-9a-f]*'),
    resolved_ordinal INTEGER NOT NULL CHECK (resolved_ordinal >= 0),
    processing_interest_id TEXT CHECK (processing_interest_id IS NULL OR length(processing_interest_id) = 32),
    processing_job_id TEXT CHECK (processing_job_id IS NULL OR length(processing_job_id) = 32),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'ready', 'failed', 'canceled', 'expired')),
    error_category TEXT NOT NULL DEFAULT '' CHECK (length(error_category) <= 64),
    idempotency_expires_at DATETIME NOT NULL,
    absolute_expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    finished_at DATETIME,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (owner_user_id, endpoint, key_digest),
    CHECK (created_at <= updated_at AND created_at <= idempotency_expires_at AND created_at <= absolute_expires_at),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_archive_member_requests_state_id
    ON backup_asset_archive_member_requests(state, id);

CREATE TABLE backup_asset_export_delivery_grants (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    delivery_id TEXT NOT NULL UNIQUE CHECK (length(delivery_id) = 32 AND delivery_id NOT GLOB '*[^0-9a-f]*'),
    resource_kind TEXT NOT NULL CHECK (resource_kind IN ('export_archive', 'archive_member')),
    export_job_id TEXT,
    export_artifact_id TEXT,
    export_attempt_id TEXT,
    export_fence_digest TEXT NOT NULL DEFAULT '',
    selection_digest TEXT NOT NULL DEFAULT '',
    artifact_digest TEXT NOT NULL DEFAULT '',
    plaintext_size INTEGER NOT NULL DEFAULT 0 CHECK (plaintext_size >= 0),
    ciphertext_size INTEGER NOT NULL DEFAULT 0 CHECK (ciphertext_size >= 0),
    format_version INTEGER NOT NULL DEFAULT 0 CHECK (format_version >= 0),
    chunk_bytes INTEGER NOT NULL DEFAULT 0 CHECK (chunk_bytes >= 0),
    job_key_id TEXT,
    job_key_version INTEGER NOT NULL DEFAULT 0 CHECK (job_key_version >= 0),
    member_request_id TEXT,
    outer_recovery_point_id TEXT NOT NULL DEFAULT '',
    outer_entry_id TEXT NOT NULL DEFAULT '',
    outer_source_fingerprint TEXT NOT NULL DEFAULT '',
    outer_entry_fingerprint TEXT NOT NULL DEFAULT '',
    member_chain_digest TEXT NOT NULL DEFAULT '',
    processing_job_id TEXT,
    processing_attempt_id TEXT,
    derived_artifact_set_id TEXT,
    derived_artifact_id TEXT,
    derived_blob_id TEXT,
    derived_digest TEXT NOT NULL DEFAULT '',
    derived_size INTEGER NOT NULL DEFAULT 0 CHECK (derived_size >= 0),
    owner_user_id INTEGER NOT NULL,
    session_jti TEXT NOT NULL CHECK (length(session_jti) BETWEEN 1 AND 128),
    token_version INTEGER NOT NULL CHECK (token_version >= 0),
    role_revision INTEGER NOT NULL CHECK (role_revision >= 0),
    proof_action TEXT NOT NULL CHECK (proof_action IN ('asset.export_download', 'asset.download')),
    proof_id TEXT NOT NULL CHECK (length(proof_id) BETWEEN 1 AND 64),
    proof_expires_at DATETIME NOT NULL,
    cookie_secret_hash TEXT NOT NULL CHECK (length(cookie_secret_hash) = 64 AND cookie_secret_hash NOT GLOB '*[^0-9a-f]*'),
    action TEXT NOT NULL CHECK (action IN ('export_download', 'archive_member_download')),
    canonical_path TEXT NOT NULL CHECK (length(canonical_path) BETWEEN 1 AND 255),
    method_policy TEXT NOT NULL CHECK (method_policy = 'get_head'),
    range_policy TEXT NOT NULL CHECK (range_policy IN ('none', 'single')),
    state TEXT NOT NULL CHECK (state IN ('issued', 'active', 'draining', 'revoked', 'expired', 'closed')),
    revoke_reason TEXT NOT NULL DEFAULT '' CHECK (length(revoke_reason) <= 64),
    idle_expires_at DATETIME NOT NULL,
    absolute_expires_at DATETIME NOT NULL,
    max_requests INTEGER NOT NULL CHECK (max_requests > 0),
    max_cumulative_bytes INTEGER NOT NULL CHECK (max_cumulative_bytes > 0),
    max_in_flight INTEGER NOT NULL CHECK (max_in_flight > 0),
    request_count INTEGER NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    consumed_bytes INTEGER NOT NULL DEFAULT 0 CHECK (consumed_bytes >= 0),
    in_flight INTEGER NOT NULL DEFAULT 0 CHECK (in_flight >= 0),
    audit_state TEXT NOT NULL DEFAULT 'none' CHECK (audit_state IN ('none', 'pending', 'emitted', 'retry_wait', 'failed')),
    audit_range_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_range_count >= 0),
    audit_range_bytes INTEGER NOT NULL DEFAULT 0 CHECK (audit_range_bytes >= 0),
    audit_request_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_request_count >= 0),
    audit_success_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_success_count >= 0),
    audit_blocked_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_blocked_count >= 0),
    audit_failure_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_failure_count >= 0),
    audit_failure_code TEXT NOT NULL DEFAULT '' CHECK (audit_failure_code IN ('', 'audit_write_failed', 'reconciliation_failed')),
    audit_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (audit_attempt_count >= 0),
    audit_next_attempt_at DATETIME,
    issued_at DATETIME NOT NULL,
    last_access_at DATETIME,
    revoked_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (issued_at <= idle_expires_at AND idle_expires_at <= absolute_expires_at AND absolute_expires_at <= proof_expires_at),
    CHECK (request_count <= max_requests AND consumed_bytes + reserved_bytes <= max_cumulative_bytes AND in_flight <= max_in_flight),
    CHECK (
        (audit_state = 'none'
         AND audit_range_count = 0 AND audit_range_bytes = 0 AND audit_request_count = 0
         AND audit_success_count = 0 AND audit_blocked_count = 0 AND audit_failure_count = 0
         AND audit_failure_code = '' AND audit_attempt_count = 0 AND audit_next_attempt_at IS NULL)
        OR
        (audit_state IN ('pending', 'emitted', 'retry_wait', 'failed')
         AND audit_request_count > 0 AND audit_request_count <= request_count
         AND audit_range_count <= audit_request_count
         AND (audit_range_count > 0 OR audit_range_bytes = 0)
         AND audit_range_bytes <= consumed_bytes
         AND audit_success_count + audit_blocked_count + audit_failure_count = audit_request_count
         AND ((audit_state = 'pending' AND audit_failure_code = '' AND audit_attempt_count = 0 AND audit_next_attempt_at IS NULL)
              OR (audit_state = 'emitted' AND audit_failure_code = '' AND audit_next_attempt_at IS NULL)
              OR (audit_state IN ('retry_wait', 'failed') AND audit_failure_code IN ('audit_write_failed', 'reconciliation_failed')
                  AND audit_attempt_count > 0 AND audit_next_attempt_at IS NOT NULL)))
    ),
    CHECK (
        (resource_kind = 'export_archive'
         AND export_job_id IS NOT NULL AND export_artifact_id IS NOT NULL AND export_attempt_id IS NOT NULL
         AND length(export_fence_digest) = 64 AND length(selection_digest) = 64 AND length(artifact_digest) = 64
         AND plaintext_size >= 0 AND ciphertext_size > 0 AND format_version > 0 AND chunk_bytes > 0
         AND job_key_id IS NOT NULL AND job_key_version > 0 AND member_request_id IS NULL
         AND outer_recovery_point_id = '' AND outer_entry_id = '' AND outer_source_fingerprint = ''
         AND outer_entry_fingerprint = '' AND member_chain_digest = '' AND processing_job_id IS NULL
         AND processing_attempt_id IS NULL AND derived_artifact_set_id IS NULL AND derived_artifact_id IS NULL
         AND derived_blob_id IS NULL AND derived_digest = '' AND derived_size = 0
         AND proof_action = 'asset.export_download' AND action = 'export_download' AND range_policy = 'single')
        OR
        (resource_kind = 'archive_member'
         AND export_job_id IS NULL AND export_artifact_id IS NULL AND export_attempt_id IS NULL
         AND export_fence_digest = '' AND selection_digest = '' AND artifact_digest = ''
         AND plaintext_size = 0 AND ciphertext_size = 0 AND format_version = 0 AND chunk_bytes = 0
         AND job_key_id IS NULL AND job_key_version = 0 AND member_request_id IS NOT NULL
         AND length(outer_recovery_point_id) = 32 AND length(outer_entry_id) = 64
         AND length(outer_source_fingerprint) BETWEEN 1 AND 128 AND length(outer_entry_fingerprint) BETWEEN 1 AND 128
         AND length(member_chain_digest) = 64
         AND processing_job_id IS NOT NULL AND length(processing_job_id) = 32 AND processing_job_id NOT GLOB '*[^0-9a-f]*'
         AND processing_attempt_id IS NOT NULL AND length(processing_attempt_id) = 32 AND processing_attempt_id NOT GLOB '*[^0-9a-f]*'
         AND derived_artifact_set_id IS NOT NULL AND length(derived_artifact_set_id) = 32 AND derived_artifact_set_id NOT GLOB '*[^0-9a-f]*'
         AND derived_artifact_id IS NOT NULL AND length(derived_artifact_id) = 32 AND derived_artifact_id NOT GLOB '*[^0-9a-f]*'
         AND derived_blob_id IS NOT NULL AND length(derived_blob_id) = 32 AND derived_blob_id NOT GLOB '*[^0-9a-f]*'
         AND length(derived_digest) = 64 AND derived_size >= 0
         AND proof_action = 'asset.download' AND action = 'archive_member_download' AND range_policy = 'none')
    ),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (export_job_id) REFERENCES backup_asset_export_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (export_artifact_id) REFERENCES backup_asset_export_artifacts(id) ON DELETE RESTRICT,
    FOREIGN KEY (export_attempt_id) REFERENCES backup_asset_export_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (job_key_id) REFERENCES backup_asset_export_keys(id) ON DELETE RESTRICT,
    FOREIGN KEY (member_request_id) REFERENCES backup_asset_archive_member_requests(id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_export_delivery_grants_session
    ON backup_asset_export_delivery_grants(owner_user_id, session_jti, state);
CREATE INDEX idx_backup_asset_export_delivery_grants_expiry
    ON backup_asset_export_delivery_grants(state, idle_expires_at, absolute_expires_at);
CREATE INDEX idx_backup_asset_export_delivery_grants_audit
    ON backup_asset_export_delivery_grants(audit_state, audit_next_attempt_at, updated_at);
CREATE INDEX idx_backup_asset_export_delivery_grants_member_state
    ON backup_asset_export_delivery_grants(member_request_id, resource_kind, state, id);
CREATE INDEX idx_backup_asset_export_delivery_grants_export_job
    ON backup_asset_export_delivery_grants(export_job_id, resource_kind, id);

CREATE TABLE backup_asset_export_delivery_requests (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    grant_id TEXT NOT NULL CHECK (length(grant_id) = 32),
    method TEXT NOT NULL CHECK (method IN ('GET', 'HEAD')),
    range_requested INTEGER NOT NULL DEFAULT 0 CHECK (range_requested IN (0, 1)),
    range_offset INTEGER CHECK (range_offset IS NULL OR range_offset >= 0),
    range_length INTEGER CHECK (range_length IS NULL OR range_length > 0),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'streaming', 'succeeded', 'blocked', 'canceled', 'failed', 'reconciled')),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    plaintext_bytes INTEGER NOT NULL DEFAULT 0 CHECK (plaintext_bytes >= 0),
    ciphertext_bytes INTEGER NOT NULL DEFAULT 0 CHECK (ciphertext_bytes >= 0),
    failure_code TEXT NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
    started_at DATETIME NOT NULL,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((range_offset IS NULL AND range_length IS NULL) OR
           (range_requested = 1 AND range_offset IS NOT NULL AND range_length IS NOT NULL)),
    CHECK (method <> 'HEAD' OR reserved_bytes = 0),
    FOREIGN KEY (grant_id) REFERENCES backup_asset_export_delivery_grants(id) ON DELETE RESTRICT
);
CREATE INDEX idx_backup_asset_export_delivery_requests_grant_state
    ON backup_asset_export_delivery_requests(grant_id, state, started_at);
