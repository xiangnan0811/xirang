BEGIN;

ALTER TABLE wrapped_domain_keys DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store'));

CREATE TABLE backup_asset_worker_identities (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    transport_kind VARCHAR(16) NOT NULL CHECK (transport_kind IN ('local', 'mtls')),
    transport_fingerprint VARCHAR(64) NOT NULL CHECK (transport_fingerprint ~ '^[0-9a-f]{64}$'),
    instance_id VARCHAR(32) NOT NULL CHECK (instance_id ~ '^[0-9a-f]{32}$'),
    identity_revision BIGINT NOT NULL DEFAULT 1 CHECK (identity_revision > 0),
    protocol_version INTEGER NOT NULL CHECK (protocol_version > 0),
    trust_state VARCHAR(16) NOT NULL CHECK (trust_state IN ('active', 'quarantined', 'revoked')),
    health_state VARCHAR(16) NOT NULL CHECK (health_state IN ('ready', 'degraded', 'draining')),
    interactive_slots INTEGER NOT NULL DEFAULT 0 CHECK (interactive_slots BETWEEN 0 AND 64),
    background_slots INTEGER NOT NULL DEFAULT 0 CHECK (background_slots BETWEEN 0 AND 64),
    quarantine_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (quarantine_code IN ('', 'protocol_incompatible', 'invalid_output', 'digest_mismatch', 'sandbox_violation', 'network_violation')),
    last_seen_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT backup_asset_worker_identity_quarantine_check CHECK (
        (trust_state = 'quarantined' AND quarantine_code <> '')
        OR (trust_state <> 'quarantined' AND quarantine_code = '')
    ),
    UNIQUE (transport_kind, transport_fingerprint, instance_id, identity_revision)
);

CREATE INDEX idx_backup_asset_workers_trust_health
    ON backup_asset_worker_identities(trust_state, health_state, last_seen_at);

CREATE TABLE backup_asset_worker_capabilities (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    worker_id VARCHAR(32) NOT NULL CHECK (worker_id ~ '^[0-9a-f]{32}$'),
    capability VARCHAR(64) NOT NULL CHECK (char_length(capability) BETWEEN 1 AND 64),
    capability_schema VARCHAR(64) NOT NULL CHECK (char_length(capability_schema) BETWEEN 1 AND 64),
    pipeline_fingerprint VARCHAR(128) NOT NULL CHECK (char_length(pipeline_fingerprint) BETWEEN 1 AND 128),
    output_profile VARCHAR(64) NOT NULL CHECK (char_length(output_profile) BETWEEN 1 AND 64),
    input_modes VARCHAR(128) NOT NULL CHECK (char_length(input_modes) BETWEEN 1 AND 128),
    limits_canonical BYTEA NOT NULL CHECK (octet_length(limits_canonical) BETWEEN 1 AND 4096),
    advertisement_digest VARCHAR(64) NOT NULL CHECK (advertisement_digest ~ '^[0-9a-f]{64}$'),
    health_state VARCHAR(16) NOT NULL CHECK (health_state IN ('ready', 'degraded', 'draining')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (worker_id, capability, capability_schema, pipeline_fingerprint, output_profile),
    FOREIGN KEY (worker_id) REFERENCES backup_asset_worker_identities(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_worker_capabilities_match
    ON backup_asset_worker_capabilities(capability, capability_schema, pipeline_fingerprint, output_profile, health_state);

CREATE TABLE backup_asset_processing_jobs (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    work_key VARCHAR(64) NOT NULL CHECK (work_key ~ '^[0-9a-f]{64}$'),
    descriptor_schema_version INTEGER NOT NULL CHECK (descriptor_schema_version = 1),
    descriptor_canonical BYTEA NOT NULL CHECK (octet_length(descriptor_canonical) BETWEEN 1 AND 65536),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$'),
    catalog_generation_id VARCHAR(32) NOT NULL CHECK (catalog_generation_id ~ '^[0-9a-f]{32}$'),
    entry_id VARCHAR(64) NOT NULL CHECK (char_length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint VARCHAR(128) NOT NULL CHECK (char_length(source_fingerprint) BETWEEN 1 AND 128),
    entry_fingerprint VARCHAR(128) NOT NULL DEFAULT '' CHECK (char_length(entry_fingerprint) <= 128),
    provider_capability_revision BIGINT NOT NULL CHECK (provider_capability_revision > 0),
    capability VARCHAR(64) NOT NULL CHECK (char_length(capability) BETWEEN 1 AND 64),
    capability_schema VARCHAR(64) NOT NULL CHECK (char_length(capability_schema) BETWEEN 1 AND 64),
    pipeline_fingerprint VARCHAR(128) NOT NULL CHECK (char_length(pipeline_fingerprint) BETWEEN 1 AND 128),
    output_profile VARCHAR(64) NOT NULL CHECK (char_length(output_profile) BETWEEN 1 AND 64),
    security_policy_revision VARCHAR(128) NOT NULL CHECK (char_length(security_policy_revision) BETWEEN 1 AND 128),
    priority_class VARCHAR(16) NOT NULL CHECK (priority_class IN ('interactive', 'background')),
    effective_priority INTEGER NOT NULL CHECK (effective_priority BETWEEN 0 AND 1000),
    state VARCHAR(32) NOT NULL CHECK (state IN ('queued', 'leased', 'fetching', 'materializing', 'processing', 'uploading', 'validating', 'retry_wait', 'cancel_requested', 'canceled', 'succeeded', 'failed', 'superseded', 'expired')),
    transition_revision BIGINT NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    error_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (error_code IN ('', 'unsupported_format', 'encrypted_archive', 'input_too_large', 'materialization_disabled', 'source_changed', 'source_expired', 'worker_unavailable', 'provider_unavailable', 'quota_busy', 'timeout', 'worker_crash', 'lease_lost', 'protocol_incompatible', 'invalid_output', 'digest_mismatch', 'sandbox_violation', 'network_violation')),
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count BETWEEN 0 AND 20),
    retry_at TIMESTAMPTZ,
    cancel_reason VARCHAR(64) NOT NULL DEFAULT '' CHECK (cancel_reason IN ('', 'interest_withdrawn', 'admin_requested', 'shutdown')),
    supersede_reason VARCHAR(64) NOT NULL DEFAULT '' CHECK (supersede_reason IN ('', 'source_changed', 'pipeline_changed', 'policy_changed')),
    expiry_reason VARCHAR(64) NOT NULL DEFAULT '' CHECK (expiry_reason IN ('', 'source_expired', 'recovery_point_expired', 'deadline_expired')),
    current_attempt_id VARCHAR(32),
    current_artifact_set_id VARCHAR(32),
    is_current BOOLEAN NOT NULL DEFAULT TRUE,
    queued_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    absolute_deadline TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT backup_asset_processing_jobs_retry_check CHECK (
        (state = 'retry_wait' AND error_code IN ('worker_unavailable', 'provider_unavailable', 'quota_busy', 'timeout', 'worker_crash', 'lease_lost') AND retry_at IS NOT NULL)
        OR (state = 'failed' AND error_code <> '' AND retry_at IS NULL)
        OR (state NOT IN ('retry_wait', 'failed') AND error_code = '' AND retry_at IS NULL)
    ),
    CONSTRAINT backup_asset_processing_jobs_terminal_check CHECK (
        (state IN ('canceled', 'succeeded', 'failed', 'superseded', 'expired') AND finished_at IS NOT NULL AND is_current = FALSE)
        OR (state NOT IN ('canceled', 'succeeded', 'failed', 'superseded', 'expired') AND finished_at IS NULL)
    ),
    CONSTRAINT backup_asset_processing_jobs_reason_check CHECK (
        ((state IN ('cancel_requested', 'canceled') AND cancel_reason <> '') OR (state NOT IN ('cancel_requested', 'canceled') AND cancel_reason = ''))
        AND ((state = 'superseded' AND supersede_reason <> '') OR (state <> 'superseded' AND supersede_reason = ''))
        AND ((state = 'expired' AND expiry_reason <> '') OR (state <> 'expired' AND expiry_reason = ''))
    ),
    CONSTRAINT backup_asset_processing_jobs_time_check CHECK (
        queued_at <= absolute_deadline
        AND (started_at IS NULL OR started_at >= queued_at)
        AND (finished_at IS NULL OR finished_at >= queued_at)
    ),
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX idx_backup_asset_processing_jobs_current_work
    ON backup_asset_processing_jobs(work_key) WHERE is_current = TRUE;
CREATE INDEX idx_backup_asset_processing_jobs_queue
    ON backup_asset_processing_jobs(state, priority_class, effective_priority DESC, queued_at, id);
CREATE INDEX idx_backup_asset_processing_jobs_source
    ON backup_asset_processing_jobs(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_processing_jobs_retry
    ON backup_asset_processing_jobs(state, retry_at);

CREATE TABLE backup_asset_processing_interests (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    owner_kind VARCHAR(16) NOT NULL CHECK (owner_kind IN ('workspace', 'search', 'system')),
    owner_key VARCHAR(128) NOT NULL CHECK (char_length(owner_key) BETWEEN 1 AND 128),
    priority_class VARCHAR(16) NOT NULL CHECK (priority_class IN ('interactive', 'background')),
    priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 1000),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    removed_reason VARCHAR(32) NOT NULL DEFAULT '' CHECK (removed_reason IN ('', 'completed', 'canceled', 'expired', 'superseded')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    removed_at TIMESTAMPTZ,
    CONSTRAINT backup_asset_processing_interests_lifecycle_check CHECK (
        (active = TRUE AND removed_reason = '' AND removed_at IS NULL)
        OR (active = FALSE AND removed_reason <> '' AND removed_at IS NOT NULL)
    ),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_backup_asset_processing_interests_active
    ON backup_asset_processing_interests(job_id, owner_kind, owner_key) WHERE active = TRUE;
CREATE INDEX idx_backup_asset_processing_interests_job_priority
    ON backup_asset_processing_interests(job_id, active, priority_class, priority DESC);

CREATE TABLE backup_asset_processing_attempts (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    worker_id VARCHAR(32) NOT NULL CHECK (worker_id ~ '^[0-9a-f]{32}$'),
    slot_class VARCHAR(32) NOT NULL CHECK (slot_class IN ('interactive', 'background', 'background_borrowed')),
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'succeeded', 'failed', 'canceled', 'expired', 'superseded')),
    worker_lease_expires_at TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    recovery_point_lease_id VARCHAR(32) NOT NULL CHECK (recovery_point_lease_id ~ '^[0-9a-f]{32}$'),
    recovery_point_attempt_id VARCHAR(32) NOT NULL CHECK (recovery_point_attempt_id ~ '^[0-9a-f]{32}$'),
    recovery_point_fence_hash VARCHAR(64) NOT NULL CHECK (recovery_point_fence_hash ~ '^[0-9a-f]{64}$'),
    absolute_deadline TIMESTAMPTZ NOT NULL,
    outcome_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (outcome_code IN ('', 'unsupported_format', 'encrypted_archive', 'input_too_large', 'materialization_disabled', 'source_changed', 'source_expired', 'worker_unavailable', 'provider_unavailable', 'quota_busy', 'timeout', 'worker_crash', 'lease_lost', 'protocol_incompatible', 'invalid_output', 'digest_mismatch', 'sandbox_violation', 'network_violation')),
    is_current BOOLEAN NOT NULL DEFAULT TRUE,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT backup_asset_processing_attempts_time_check CHECK (
        last_heartbeat_at >= started_at AND worker_lease_expires_at > last_heartbeat_at AND absolute_deadline >= worker_lease_expires_at
    ),
    CONSTRAINT backup_asset_processing_attempts_lifecycle_check CHECK (
        (state = 'active' AND is_current = TRUE AND finished_at IS NULL AND outcome_code = '')
        OR (state = 'failed' AND is_current = FALSE AND finished_at IS NOT NULL AND outcome_code <> '')
        OR (state = 'expired' AND is_current = FALSE AND finished_at IS NOT NULL
            AND outcome_code IN ('', 'lease_lost'))
        OR (state IN ('succeeded', 'canceled', 'superseded')
            AND is_current = FALSE AND finished_at IS NOT NULL AND outcome_code = '')
    ),
    UNIQUE (job_id, attempt_number),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (worker_id) REFERENCES backup_asset_worker_identities(id) ON DELETE RESTRICT,
    FOREIGN KEY (recovery_point_lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX idx_backup_asset_processing_attempts_current
    ON backup_asset_processing_attempts(job_id) WHERE is_current = TRUE;
CREATE INDEX idx_backup_asset_processing_attempts_worker_lease
    ON backup_asset_processing_attempts(worker_id, state, worker_lease_expires_at);

CREATE TABLE backup_asset_processing_grants (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) NOT NULL CHECK (attempt_id ~ '^[0-9a-f]{32}$'),
    worker_id VARCHAR(32) NOT NULL CHECK (worker_id ~ '^[0-9a-f]{32}$'),
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('input', 'sink')),
    activation_secret_hash VARCHAR(64) NOT NULL CHECK (activation_secret_hash = '' OR activation_secret_hash ~ '^[0-9a-f]{64}$'),
    fence_hash VARCHAR(64) NOT NULL CHECK (fence_hash ~ '^[0-9a-f]{64}$'),
    state VARCHAR(16) NOT NULL CHECK (state IN ('issued', 'active', 'revoked', 'expired', 'closed')),
    max_requests BIGINT NOT NULL CHECK (max_requests > 0),
    max_bytes_per_request BIGINT NOT NULL CHECK (max_bytes_per_request > 0),
    max_cumulative_bytes BIGINT NOT NULL CHECK (max_cumulative_bytes >= max_bytes_per_request),
    max_in_flight BIGINT NOT NULL CHECK (max_in_flight > 0),
    request_count BIGINT NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    reserved_bytes BIGINT NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    consumed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (consumed_bytes >= 0),
    in_flight BIGINT NOT NULL DEFAULT 0 CHECK (in_flight >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    activated_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revocation_reason VARCHAR(32) NOT NULL DEFAULT '' CHECK (revocation_reason IN ('', 'cancel', 'lease_lost', 'source_changed', 'expired', 'quarantine', 'shutdown')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT backup_asset_processing_grants_budget_check CHECK (
        request_count <= max_requests AND in_flight <= max_in_flight
        AND consumed_bytes <= max_cumulative_bytes
        AND reserved_bytes <= max_cumulative_bytes - consumed_bytes
    ),
    CONSTRAINT backup_asset_processing_grants_lifecycle_check CHECK (
        (state = 'issued' AND activation_secret_hash ~ '^[0-9a-f]{64}$' AND activated_at IS NULL AND revoked_at IS NULL AND revocation_reason = '')
        OR (state = 'active' AND activation_secret_hash = '' AND activated_at IS NOT NULL AND revoked_at IS NULL AND revocation_reason = '')
        OR (state = 'closed' AND activation_secret_hash = '' AND activated_at IS NOT NULL AND revoked_at IS NULL AND revocation_reason = '')
        OR (state IN ('revoked', 'expired') AND activation_secret_hash = '' AND revoked_at IS NOT NULL AND revocation_reason <> '')
    ),
    UNIQUE (attempt_id, kind),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_processing_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (worker_id) REFERENCES backup_asset_worker_identities(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_processing_grants_state_expiry
    ON backup_asset_processing_grants(state, expires_at);
CREATE INDEX idx_backup_asset_processing_grants_attempt
    ON backup_asset_processing_grants(attempt_id, kind, state);

CREATE TABLE backup_asset_processing_grant_requests (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    grant_id VARCHAR(32) NOT NULL CHECK (grant_id ~ '^[0-9a-f]{32}$'),
    request_kind VARCHAR(16) NOT NULL CHECK (request_kind IN ('stat', 'sequential', 'range', 'upload')),
    range_offset BIGINT,
    range_length BIGINT,
    state VARCHAR(16) NOT NULL CHECK (state IN ('reserved', 'streaming', 'succeeded', 'failed', 'canceled', 'reconciled')),
    reserved_bytes BIGINT NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    provider_bytes BIGINT NOT NULL DEFAULT 0 CHECK (provider_bytes >= 0 AND provider_bytes <= reserved_bytes),
    stored_bytes BIGINT NOT NULL DEFAULT 0 CHECK (stored_bytes >= 0 AND stored_bytes <= reserved_bytes),
    failure_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'budget_exhausted', 'source_changed', 'lease_lost', 'client_canceled', 'source_failed', 'write_failed', 'reconciled_crash')),
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT backup_asset_processing_grant_requests_range_check CHECK (
        (request_kind = 'range' AND range_offset IS NOT NULL AND range_offset >= 0 AND range_length IS NOT NULL AND range_length > 0)
        OR (request_kind <> 'range' AND range_offset IS NULL AND range_length IS NULL)
    ),
    CONSTRAINT backup_asset_processing_grant_requests_lifecycle_check CHECK (
        (state IN ('reserved', 'streaming') AND finished_at IS NULL AND failure_code = '')
        OR (state = 'succeeded' AND finished_at IS NOT NULL AND failure_code = '')
        OR (state IN ('failed', 'canceled', 'reconciled') AND finished_at IS NOT NULL AND failure_code <> '')
    ),
    FOREIGN KEY (grant_id) REFERENCES backup_asset_processing_grants(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_processing_grant_requests_reconcile
    ON backup_asset_processing_grant_requests(state, started_at);

CREATE TABLE backup_asset_processing_uploads (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL CHECK (job_id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) NOT NULL CHECK (attempt_id ~ '^[0-9a-f]{32}$'),
    grant_id VARCHAR(32) NOT NULL CHECK (grant_id ~ '^[0-9a-f]{32}$'),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 256),
    role VARCHAR(16) NOT NULL CHECK (role IN ('noop', 'content', 'ocr', 'thumbnail', 'metadata')),
    media_type VARCHAR(128) NOT NULL CHECK (char_length(media_type) BETWEEN 1 AND 128),
    declared_size BIGINT NOT NULL CHECK (declared_size >= 0),
    declared_digest VARCHAR(64) NOT NULL CHECK (declared_digest ~ '^[0-9a-f]{64}$'),
    actual_size BIGINT NOT NULL DEFAULT 0 CHECK (actual_size >= 0),
    actual_digest VARCHAR(64) NOT NULL DEFAULT '' CHECK (actual_digest = '' OR actual_digest ~ '^[0-9a-f]{64}$'),
    completeness VARCHAR(16) NOT NULL CHECK (completeness IN ('complete', 'partial')),
    coverage_canonical BYTEA NOT NULL CHECK (octet_length(coverage_canonical) BETWEEN 1 AND 4096),
    staging_id VARCHAR(32) NOT NULL UNIQUE CHECK (staging_id ~ '^[0-9a-f]{32}$'),
    state VARCHAR(16) NOT NULL CHECK (state IN ('reserved', 'streaming', 'staged', 'committed', 'rejected', 'orphaned')),
    failure_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'invalid_output', 'digest_mismatch', 'quota_busy', 'lease_lost', 'source_changed')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    CONSTRAINT backup_asset_processing_uploads_lifecycle_check CHECK (
        (state IN ('reserved', 'streaming') AND finished_at IS NULL AND actual_digest = '' AND failure_code = '')
        OR (state IN ('staged', 'committed') AND finished_at IS NOT NULL AND actual_size = declared_size AND actual_digest = declared_digest AND failure_code = '')
        OR (state IN ('rejected', 'orphaned') AND finished_at IS NOT NULL AND failure_code <> '')
    ),
    UNIQUE (grant_id, ordinal),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_processing_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (grant_id) REFERENCES backup_asset_processing_grants(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_processing_uploads_staging
    ON backup_asset_processing_uploads(state, updated_at);

CREATE TABLE backup_asset_derived_blobs (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    plaintext_digest VARCHAR(64) NOT NULL CHECK (plaintext_digest ~ '^[0-9a-f]{64}$'),
    plaintext_size BIGINT NOT NULL CHECK (plaintext_size >= 0),
    physical_size BIGINT NOT NULL CHECK (physical_size > 0),
    cipher_format_version INTEGER NOT NULL CHECK (cipher_format_version = 1),
    chunk_size BIGINT NOT NULL CHECK (chunk_size BETWEEN 65536 AND 8388608),
    chunk_count BIGINT NOT NULL CHECK (chunk_count > 0 AND chunk_count <= 4294967296),
    nonce_prefix BYTEA NOT NULL CHECK (octet_length(nonce_prefix) = 8),
    opaque_locator VARCHAR(128) NOT NULL UNIQUE CHECK (char_length(opaque_locator) BETWEEN 1 AND 128 AND opaque_locator !~ '[/\\]'),
    wrapped_dek BYTEA NOT NULL,
    envelope_nonce BYTEA NOT NULL CHECK (octet_length(envelope_nonce) = 12),
    derived_kek_version INTEGER NOT NULL CHECK (derived_kek_version > 0),
    state VARCHAR(16) NOT NULL CHECK (state IN ('staged', 'active', 'unavailable', 'purging', 'purge_failed')),
    ref_count BIGINT NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    unavailable_at TIMESTAMPTZ,
    CONSTRAINT backup_asset_derived_blobs_lifecycle_check CHECK (
        (state IN ('staged', 'active') AND octet_length(wrapped_dek) > 0 AND unavailable_at IS NULL)
        OR (state IN ('unavailable', 'purging', 'purge_failed') AND octet_length(wrapped_dek) = 0 AND unavailable_at IS NOT NULL)
    ),
    UNIQUE (plaintext_digest, plaintext_size, cipher_format_version, chunk_size)
);

CREATE INDEX idx_backup_asset_derived_blobs_state
    ON backup_asset_derived_blobs(state, updated_at);
CREATE INDEX idx_backup_asset_derived_blobs_kek
    ON backup_asset_derived_blobs(derived_kek_version, state);

CREATE TABLE backup_asset_derived_artifact_sets (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    job_id VARCHAR(32) NOT NULL UNIQUE CHECK (job_id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) NOT NULL CHECK (attempt_id ~ '^[0-9a-f]{32}$'),
    work_key VARCHAR(64) NOT NULL CHECK (work_key ~ '^[0-9a-f]{64}$'),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$'),
    catalog_generation_id VARCHAR(32) NOT NULL CHECK (catalog_generation_id ~ '^[0-9a-f]{32}$'),
    entry_id VARCHAR(64) NOT NULL CHECK (char_length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint VARCHAR(128) NOT NULL CHECK (char_length(source_fingerprint) BETWEEN 1 AND 128),
    security_policy_revision VARCHAR(128) NOT NULL CHECK (char_length(security_policy_revision) BETWEEN 1 AND 128),
    manifest_digest VARCHAR(64) NOT NULL UNIQUE CHECK (manifest_digest ~ '^[0-9a-f]{64}$'),
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'stale', 'unavailable', 'superseded', 'revoked', 'purging', 'purge_failed')),
    revocation_reason VARCHAR(32) NOT NULL DEFAULT '' CHECK (revocation_reason IN ('', 'explicit', 'expired', 'source_changed', 'policy_changed', 'key_loss', 'rollback')),
    completeness VARCHAR(16) NOT NULL CHECK (completeness IN ('complete', 'partial')),
    artifact_count INTEGER NOT NULL CHECK (artifact_count > 0 AND artifact_count <= 256),
    total_plaintext_bytes BIGINT NOT NULL CHECK (total_plaintext_bytes >= 0),
    projection_required BOOLEAN NOT NULL DEFAULT FALSE,
    projection_published BOOLEAN NOT NULL DEFAULT FALSE,
    projection_revision BIGINT NOT NULL DEFAULT 0 CHECK (projection_revision >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT backup_asset_derived_sets_projection_check CHECK (projection_published = FALSE OR projection_required = TRUE),
    CONSTRAINT backup_asset_derived_sets_lifecycle_check CHECK (
        (state IN ('active', 'stale') AND revocation_reason = '' AND revoked_at IS NULL)
        OR (state IN ('unavailable', 'superseded', 'revoked', 'purging', 'purge_failed') AND revocation_reason <> '' AND revoked_at IS NOT NULL)
    ),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_processing_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_derived_sets_source_state
    ON backup_asset_derived_artifact_sets(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_derived_sets_work_state
    ON backup_asset_derived_artifact_sets(work_key, state);

CREATE TABLE backup_asset_derived_artifacts (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    artifact_set_id VARCHAR(32) NOT NULL CHECK (artifact_set_id ~ '^[0-9a-f]{32}$'),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 256),
    role VARCHAR(16) NOT NULL CHECK (role IN ('noop', 'content', 'ocr', 'thumbnail', 'metadata')),
    media_type VARCHAR(128) NOT NULL CHECK (char_length(media_type) BETWEEN 1 AND 128),
    plaintext_size BIGINT NOT NULL CHECK (plaintext_size >= 0),
    plaintext_digest VARCHAR(64) NOT NULL CHECK (plaintext_digest ~ '^[0-9a-f]{64}$'),
    completeness VARCHAR(16) NOT NULL CHECK (completeness IN ('complete', 'partial')),
    coverage_canonical BYTEA NOT NULL CHECK (octet_length(coverage_canonical) BETWEEN 1 AND 4096),
    blob_id VARCHAR(32) NOT NULL CHECK (blob_id ~ '^[0-9a-f]{32}$'),
    excerpt_ref VARCHAR(128) NOT NULL DEFAULT '' CHECK (char_length(excerpt_ref) <= 128),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (artifact_set_id, ordinal),
    FOREIGN KEY (artifact_set_id) REFERENCES backup_asset_derived_artifact_sets(id) ON DELETE RESTRICT,
    FOREIGN KEY (blob_id) REFERENCES backup_asset_derived_blobs(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_derived_artifacts_blob
    ON backup_asset_derived_artifacts(blob_id, artifact_set_id);

CREATE TABLE backup_asset_derived_blob_references (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    blob_id VARCHAR(32) NOT NULL CHECK (blob_id ~ '^[0-9a-f]{32}$'),
    artifact_id VARCHAR(32) NOT NULL CHECK (artifact_id ~ '^[0-9a-f]{32}$'),
    recovery_point_id VARCHAR(32) NOT NULL CHECK (recovery_point_id ~ '^[0-9a-f]{32}$'),
    catalog_generation_id VARCHAR(32) NOT NULL CHECK (catalog_generation_id ~ '^[0-9a-f]{32}$'),
    entry_id VARCHAR(64) NOT NULL CHECK (char_length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint VARCHAR(128) NOT NULL CHECK (char_length(source_fingerprint) BETWEEN 1 AND 128),
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'unavailable', 'revoked')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT backup_asset_derived_refs_lifecycle_check CHECK (
        (state = 'active' AND revoked_at IS NULL)
        OR (state IN ('unavailable', 'revoked') AND revoked_at IS NOT NULL)
    ),
    UNIQUE (blob_id, artifact_id, recovery_point_id, catalog_generation_id, entry_id),
    FOREIGN KEY (blob_id) REFERENCES backup_asset_derived_blobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (artifact_id) REFERENCES backup_asset_derived_artifacts(id) ON DELETE RESTRICT,
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_derived_refs_source
    ON backup_asset_derived_blob_references(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_derived_refs_blob_state
    ON backup_asset_derived_blob_references(blob_id, state);

CREATE TABLE backup_asset_updater_metadata (
    id VARCHAR(32) PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    source_kind VARCHAR(32) NOT NULL CHECK (source_kind IN ('builtin', 'admin_registered')),
    source_id VARCHAR(128) NOT NULL CHECK (char_length(source_id) BETWEEN 1 AND 128),
    version VARCHAR(64) NOT NULL CHECK (char_length(version) BETWEEN 1 AND 64),
    manifest_digest VARCHAR(64) NOT NULL CHECK (manifest_digest ~ '^[0-9a-f]{64}$'),
    signing_key_fingerprint VARCHAR(64) NOT NULL CHECK (signing_key_fingerprint ~ '^[0-9a-f]{64}$'),
    bundle_fingerprint VARCHAR(64) NOT NULL CHECK (bundle_fingerprint ~ '^[0-9a-f]{64}$'),
    state VARCHAR(16) NOT NULL CHECK (state IN ('registered', 'verified', 'active', 'superseded', 'failed')),
    failure_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'invalid_signature', 'unsupported_version', 'policy_rejected', 'activation_failed')),
    verified_at TIMESTAMPTZ,
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT backup_asset_updater_metadata_lifecycle_check CHECK (
        (state = 'registered' AND verified_at IS NULL AND activated_at IS NULL AND failure_code = '')
        OR (state = 'verified' AND verified_at IS NOT NULL AND activated_at IS NULL AND failure_code = '')
        OR (state = 'active' AND verified_at IS NOT NULL AND activated_at IS NOT NULL AND failure_code = '')
        OR (state = 'superseded' AND verified_at IS NOT NULL AND failure_code = '')
        OR (state = 'failed' AND failure_code <> '')
    ),
    UNIQUE (source_kind, source_id, version)
);

CREATE INDEX idx_backup_asset_updater_metadata_state
    ON backup_asset_updater_metadata(state, updated_at);

COMMIT;
