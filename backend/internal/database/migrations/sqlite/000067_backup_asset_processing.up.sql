CREATE TABLE wrapped_domain_keys_new (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store')),
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

CREATE TABLE backup_asset_worker_identities (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    transport_kind TEXT NOT NULL CHECK (transport_kind IN ('local', 'mtls')),
    transport_fingerprint TEXT NOT NULL CHECK (length(transport_fingerprint) = 64 AND transport_fingerprint NOT GLOB '*[^0-9a-f]*'),
    instance_id TEXT NOT NULL CHECK (length(instance_id) = 32 AND instance_id NOT GLOB '*[^0-9a-f]*'),
    identity_revision INTEGER NOT NULL DEFAULT 1 CHECK (identity_revision > 0),
    protocol_version INTEGER NOT NULL CHECK (protocol_version > 0),
    trust_state TEXT NOT NULL CHECK (trust_state IN ('active', 'quarantined', 'revoked')),
    health_state TEXT NOT NULL CHECK (health_state IN ('ready', 'degraded', 'draining')),
    interactive_slots INTEGER NOT NULL DEFAULT 0 CHECK (interactive_slots >= 0 AND interactive_slots <= 64),
    background_slots INTEGER NOT NULL DEFAULT 0 CHECK (background_slots >= 0 AND background_slots <= 64),
    quarantine_code TEXT NOT NULL DEFAULT '' CHECK (quarantine_code IN ('', 'protocol_incompatible', 'invalid_output', 'digest_mismatch', 'sandbox_violation', 'network_violation')),
    last_seen_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((trust_state = 'quarantined' AND quarantine_code <> '') OR (trust_state <> 'quarantined' AND quarantine_code = '')),
    UNIQUE (transport_kind, transport_fingerprint, instance_id, identity_revision)
);

CREATE INDEX idx_backup_asset_workers_trust_health
    ON backup_asset_worker_identities(trust_state, health_state, last_seen_at);

CREATE TABLE backup_asset_worker_capabilities (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    worker_id TEXT NOT NULL CHECK (length(worker_id) = 32),
    capability TEXT NOT NULL CHECK (length(capability) BETWEEN 1 AND 64),
    capability_schema TEXT NOT NULL CHECK (length(capability_schema) BETWEEN 1 AND 64),
    pipeline_fingerprint TEXT NOT NULL CHECK (length(pipeline_fingerprint) BETWEEN 1 AND 128),
    output_profile TEXT NOT NULL CHECK (length(output_profile) BETWEEN 1 AND 64),
    input_modes TEXT NOT NULL CHECK (length(input_modes) BETWEEN 1 AND 128),
    limits_canonical BLOB NOT NULL CHECK (length(limits_canonical) BETWEEN 1 AND 4096),
    advertisement_digest TEXT NOT NULL CHECK (length(advertisement_digest) = 64 AND advertisement_digest NOT GLOB '*[^0-9a-f]*'),
    health_state TEXT NOT NULL CHECK (health_state IN ('ready', 'degraded', 'draining')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (worker_id, capability, capability_schema, pipeline_fingerprint, output_profile),
    FOREIGN KEY (worker_id) REFERENCES backup_asset_worker_identities(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_worker_capabilities_match
    ON backup_asset_worker_capabilities(capability, capability_schema, pipeline_fingerprint, output_profile, health_state);

CREATE TABLE backup_asset_processing_jobs (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    work_key TEXT NOT NULL CHECK (length(work_key) = 64 AND work_key NOT GLOB '*[^0-9a-f]*'),
    descriptor_schema_version INTEGER NOT NULL CHECK (descriptor_schema_version = 1),
    descriptor_canonical BLOB NOT NULL CHECK (length(descriptor_canonical) BETWEEN 1 AND 65536),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    entry_fingerprint TEXT NOT NULL DEFAULT '' CHECK (length(entry_fingerprint) <= 128),
    provider_capability_revision INTEGER NOT NULL CHECK (provider_capability_revision > 0),
    capability TEXT NOT NULL CHECK (length(capability) BETWEEN 1 AND 64),
    capability_schema TEXT NOT NULL CHECK (length(capability_schema) BETWEEN 1 AND 64),
    pipeline_fingerprint TEXT NOT NULL CHECK (length(pipeline_fingerprint) BETWEEN 1 AND 128),
    output_profile TEXT NOT NULL CHECK (length(output_profile) BETWEEN 1 AND 64),
    security_policy_revision TEXT NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 128),
    priority_class TEXT NOT NULL CHECK (priority_class IN ('interactive', 'background')),
    effective_priority INTEGER NOT NULL CHECK (effective_priority BETWEEN 0 AND 1000),
    state TEXT NOT NULL CHECK (state IN ('queued', 'leased', 'fetching', 'materializing', 'processing', 'uploading', 'validating', 'retry_wait', 'cancel_requested', 'canceled', 'succeeded', 'failed', 'superseded', 'expired')),
    transition_revision INTEGER NOT NULL DEFAULT 1 CHECK (transition_revision > 0),
    error_code TEXT NOT NULL DEFAULT '' CHECK (error_code IN ('', 'unsupported_format', 'encrypted_archive', 'input_too_large', 'materialization_disabled', 'source_changed', 'source_expired', 'worker_unavailable', 'provider_unavailable', 'quota_busy', 'timeout', 'worker_crash', 'lease_lost', 'protocol_incompatible', 'invalid_output', 'digest_mismatch', 'sandbox_violation', 'network_violation')),
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0 AND retry_count <= 20),
    retry_at DATETIME,
    cancel_reason TEXT NOT NULL DEFAULT '' CHECK (cancel_reason IN ('', 'interest_withdrawn', 'admin_requested', 'shutdown')),
    supersede_reason TEXT NOT NULL DEFAULT '' CHECK (supersede_reason IN ('', 'source_changed', 'pipeline_changed', 'policy_changed')),
    expiry_reason TEXT NOT NULL DEFAULT '' CHECK (expiry_reason IN ('', 'source_expired', 'recovery_point_expired', 'deadline_expired')),
    current_attempt_id TEXT,
    current_artifact_set_id TEXT,
    is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
    queued_at DATETIME NOT NULL,
    started_at DATETIME,
    finished_at DATETIME,
    absolute_deadline DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (
        (state = 'retry_wait' AND error_code IN ('worker_unavailable', 'provider_unavailable', 'quota_busy', 'timeout', 'worker_crash', 'lease_lost') AND retry_at IS NOT NULL)
        OR (state = 'failed' AND error_code <> '' AND retry_at IS NULL)
        OR (state NOT IN ('retry_wait', 'failed') AND error_code = '' AND retry_at IS NULL)
    ),
    CHECK ((state IN ('canceled', 'succeeded', 'failed', 'superseded', 'expired') AND finished_at IS NOT NULL AND is_current = 0)
        OR (state NOT IN ('canceled', 'succeeded', 'failed', 'superseded', 'expired') AND finished_at IS NULL)),
    CHECK ((state IN ('cancel_requested', 'canceled') AND cancel_reason <> '') OR (state NOT IN ('cancel_requested', 'canceled') AND cancel_reason = '')),
    CHECK ((state = 'superseded' AND supersede_reason <> '') OR (state <> 'superseded' AND supersede_reason = '')),
    CHECK ((state = 'expired' AND expiry_reason <> '') OR (state <> 'expired' AND expiry_reason = '')),
    CHECK (queued_at <= absolute_deadline AND (started_at IS NULL OR started_at >= queued_at) AND (finished_at IS NULL OR finished_at >= queued_at)),
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX idx_backup_asset_processing_jobs_current_work
    ON backup_asset_processing_jobs(work_key) WHERE is_current = 1;
CREATE INDEX idx_backup_asset_processing_jobs_queue
    ON backup_asset_processing_jobs(state, priority_class, effective_priority DESC, queued_at, id);
CREATE INDEX idx_backup_asset_processing_jobs_source
    ON backup_asset_processing_jobs(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_processing_jobs_retry
    ON backup_asset_processing_jobs(state, retry_at);

CREATE TABLE backup_asset_processing_interests (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    owner_kind TEXT NOT NULL CHECK (owner_kind IN ('workspace', 'search', 'system')),
    owner_key TEXT NOT NULL CHECK (length(owner_key) BETWEEN 1 AND 128),
    priority_class TEXT NOT NULL CHECK (priority_class IN ('interactive', 'background')),
    priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 1000),
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    removed_reason TEXT NOT NULL DEFAULT '' CHECK (removed_reason IN ('', 'completed', 'canceled', 'expired', 'superseded')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    removed_at DATETIME,
    CHECK ((active = 1 AND removed_reason = '' AND removed_at IS NULL) OR (active = 0 AND removed_reason <> '' AND removed_at IS NOT NULL)),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_backup_asset_processing_interests_active
    ON backup_asset_processing_interests(job_id, owner_kind, owner_key) WHERE active = 1;
CREATE INDEX idx_backup_asset_processing_interests_job_priority
    ON backup_asset_processing_interests(job_id, active, priority_class, priority DESC);

CREATE TABLE backup_asset_processing_attempts (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    worker_id TEXT NOT NULL CHECK (length(worker_id) = 32),
    slot_class TEXT NOT NULL CHECK (slot_class IN ('interactive', 'background', 'background_borrowed')),
    state TEXT NOT NULL CHECK (state IN ('active', 'succeeded', 'failed', 'canceled', 'expired', 'superseded')),
    worker_lease_expires_at DATETIME NOT NULL,
    last_heartbeat_at DATETIME NOT NULL,
    recovery_point_lease_id TEXT NOT NULL CHECK (length(recovery_point_lease_id) = 32),
    recovery_point_attempt_id TEXT NOT NULL CHECK (length(recovery_point_attempt_id) = 32),
    recovery_point_fence_hash TEXT NOT NULL CHECK (length(recovery_point_fence_hash) = 64 AND recovery_point_fence_hash NOT GLOB '*[^0-9a-f]*'),
    absolute_deadline DATETIME NOT NULL,
    outcome_code TEXT NOT NULL DEFAULT '' CHECK (outcome_code IN ('', 'unsupported_format', 'encrypted_archive', 'input_too_large', 'materialization_disabled', 'source_changed', 'source_expired', 'worker_unavailable', 'provider_unavailable', 'quota_busy', 'timeout', 'worker_crash', 'lease_lost', 'protocol_incompatible', 'invalid_output', 'digest_mismatch', 'sandbox_violation', 'network_violation')),
    is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
    started_at DATETIME NOT NULL,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (last_heartbeat_at >= started_at AND worker_lease_expires_at > last_heartbeat_at AND absolute_deadline >= worker_lease_expires_at),
    CHECK ((state = 'active' AND is_current = 1 AND finished_at IS NULL AND outcome_code = '')
        OR (state = 'failed' AND is_current = 0 AND finished_at IS NOT NULL AND outcome_code <> '')
        OR (state = 'expired' AND is_current = 0 AND finished_at IS NOT NULL
            AND outcome_code IN ('', 'lease_lost'))
        OR (state IN ('succeeded', 'canceled', 'superseded')
            AND is_current = 0 AND finished_at IS NOT NULL AND outcome_code = '')),
    UNIQUE (job_id, attempt_number),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (worker_id) REFERENCES backup_asset_worker_identities(id) ON DELETE RESTRICT,
    FOREIGN KEY (recovery_point_lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX idx_backup_asset_processing_attempts_current
    ON backup_asset_processing_attempts(job_id) WHERE is_current = 1;
CREATE INDEX idx_backup_asset_processing_attempts_worker_lease
    ON backup_asset_processing_attempts(worker_id, state, worker_lease_expires_at);

CREATE TABLE backup_asset_processing_grants (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32),
    worker_id TEXT NOT NULL CHECK (length(worker_id) = 32),
    kind TEXT NOT NULL CHECK (kind IN ('input', 'sink')),
    activation_secret_hash TEXT NOT NULL CHECK (length(activation_secret_hash) IN (0, 64) AND activation_secret_hash NOT GLOB '*[^0-9a-f]*'),
    fence_hash TEXT NOT NULL CHECK (length(fence_hash) = 64 AND fence_hash NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('issued', 'active', 'revoked', 'expired', 'closed')),
    max_requests INTEGER NOT NULL CHECK (max_requests > 0),
    max_bytes_per_request INTEGER NOT NULL CHECK (max_bytes_per_request > 0),
    max_cumulative_bytes INTEGER NOT NULL CHECK (max_cumulative_bytes >= max_bytes_per_request),
    max_in_flight INTEGER NOT NULL CHECK (max_in_flight > 0),
    request_count INTEGER NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    consumed_bytes INTEGER NOT NULL DEFAULT 0 CHECK (consumed_bytes >= 0),
    in_flight INTEGER NOT NULL DEFAULT 0 CHECK (in_flight >= 0),
    expires_at DATETIME NOT NULL,
    activated_at DATETIME,
    revoked_at DATETIME,
    revocation_reason TEXT NOT NULL DEFAULT '' CHECK (revocation_reason IN ('', 'cancel', 'lease_lost', 'source_changed', 'expired', 'quarantine', 'shutdown')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (request_count <= max_requests AND in_flight <= max_in_flight AND consumed_bytes <= max_cumulative_bytes AND reserved_bytes <= max_cumulative_bytes - consumed_bytes),
    CHECK ((state = 'issued' AND length(activation_secret_hash) = 64 AND activated_at IS NULL AND revoked_at IS NULL AND revocation_reason = '')
        OR (state = 'active' AND activation_secret_hash = '' AND activated_at IS NOT NULL AND revoked_at IS NULL AND revocation_reason = '')
        OR (state = 'closed' AND activation_secret_hash = '' AND activated_at IS NOT NULL AND revoked_at IS NULL AND revocation_reason = '')
        OR (state IN ('revoked', 'expired') AND activation_secret_hash = '' AND revoked_at IS NOT NULL AND revocation_reason <> '')),
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
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    grant_id TEXT NOT NULL CHECK (length(grant_id) = 32),
    request_kind TEXT NOT NULL CHECK (request_kind IN ('stat', 'sequential', 'range', 'upload')),
    range_offset INTEGER,
    range_length INTEGER,
    state TEXT NOT NULL CHECK (state IN ('reserved', 'streaming', 'succeeded', 'failed', 'canceled', 'reconciled')),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    provider_bytes INTEGER NOT NULL DEFAULT 0 CHECK (provider_bytes >= 0 AND provider_bytes <= reserved_bytes),
    stored_bytes INTEGER NOT NULL DEFAULT 0 CHECK (stored_bytes >= 0 AND stored_bytes <= reserved_bytes),
    failure_code TEXT NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'budget_exhausted', 'source_changed', 'lease_lost', 'client_canceled', 'source_failed', 'write_failed', 'reconciled_crash')),
    started_at DATETIME NOT NULL,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((request_kind = 'range' AND range_offset IS NOT NULL AND range_offset >= 0 AND range_length IS NOT NULL AND range_length > 0)
        OR (request_kind <> 'range' AND range_offset IS NULL AND range_length IS NULL)),
    CHECK ((state IN ('reserved', 'streaming') AND finished_at IS NULL AND failure_code = '')
        OR (state = 'succeeded' AND finished_at IS NOT NULL AND failure_code = '')
        OR (state IN ('failed', 'canceled', 'reconciled') AND finished_at IS NOT NULL AND failure_code <> '')),
    FOREIGN KEY (grant_id) REFERENCES backup_asset_processing_grants(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_processing_grant_requests_reconcile
    ON backup_asset_processing_grant_requests(state, started_at);

CREATE TABLE backup_asset_processing_uploads (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32),
    grant_id TEXT NOT NULL CHECK (length(grant_id) = 32),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 256),
    role TEXT NOT NULL CHECK (role IN ('noop', 'content', 'ocr', 'thumbnail', 'metadata')),
    media_type TEXT NOT NULL CHECK (length(media_type) BETWEEN 1 AND 128),
    declared_size INTEGER NOT NULL CHECK (declared_size >= 0),
    declared_digest TEXT NOT NULL CHECK (length(declared_digest) = 64 AND declared_digest NOT GLOB '*[^0-9a-f]*'),
    actual_size INTEGER NOT NULL DEFAULT 0 CHECK (actual_size >= 0),
    actual_digest TEXT NOT NULL DEFAULT '' CHECK (length(actual_digest) IN (0, 64) AND actual_digest NOT GLOB '*[^0-9a-f]*'),
    completeness TEXT NOT NULL CHECK (completeness IN ('complete', 'partial')),
    coverage_canonical BLOB NOT NULL CHECK (length(coverage_canonical) BETWEEN 1 AND 4096),
    staging_id TEXT NOT NULL CHECK (length(staging_id) = 32 AND staging_id NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'streaming', 'staged', 'committed', 'rejected', 'orphaned')),
    failure_code TEXT NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'invalid_output', 'digest_mismatch', 'quota_busy', 'lease_lost', 'source_changed')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    finished_at DATETIME,
    CHECK ((state IN ('reserved', 'streaming') AND finished_at IS NULL AND actual_digest = '' AND failure_code = '')
        OR (state IN ('staged', 'committed') AND finished_at IS NOT NULL AND actual_size = declared_size AND actual_digest = declared_digest AND failure_code = '')
        OR (state IN ('rejected', 'orphaned') AND finished_at IS NOT NULL AND failure_code <> '')),
    UNIQUE (grant_id, ordinal),
    UNIQUE (staging_id),
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_processing_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (grant_id) REFERENCES backup_asset_processing_grants(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_processing_uploads_staging
    ON backup_asset_processing_uploads(state, updated_at);

CREATE TABLE backup_asset_derived_blobs (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    plaintext_digest TEXT NOT NULL CHECK (length(plaintext_digest) = 64 AND plaintext_digest NOT GLOB '*[^0-9a-f]*'),
    plaintext_size INTEGER NOT NULL CHECK (plaintext_size >= 0),
    physical_size INTEGER NOT NULL CHECK (physical_size > 0),
    cipher_format_version INTEGER NOT NULL CHECK (cipher_format_version = 1),
    chunk_size INTEGER NOT NULL CHECK (chunk_size BETWEEN 65536 AND 8388608),
    chunk_count INTEGER NOT NULL CHECK (chunk_count > 0 AND chunk_count <= 4294967296),
    nonce_prefix BLOB NOT NULL CHECK (length(nonce_prefix) = 8),
    opaque_locator TEXT NOT NULL UNIQUE CHECK (length(opaque_locator) BETWEEN 1 AND 128 AND opaque_locator NOT GLOB '*[/\\]*'),
    wrapped_dek BLOB NOT NULL,
    envelope_nonce BLOB NOT NULL CHECK (length(envelope_nonce) = 12),
    derived_kek_version INTEGER NOT NULL CHECK (derived_kek_version > 0),
    state TEXT NOT NULL CHECK (state IN ('staged', 'active', 'unavailable', 'purging', 'purge_failed')),
    ref_count INTEGER NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    unavailable_at DATETIME,
    CHECK ((state IN ('staged', 'active') AND length(wrapped_dek) > 0 AND unavailable_at IS NULL)
        OR (state IN ('unavailable', 'purging', 'purge_failed') AND length(wrapped_dek) = 0 AND unavailable_at IS NOT NULL)),
    UNIQUE (plaintext_digest, plaintext_size, cipher_format_version, chunk_size)
);

CREATE INDEX idx_backup_asset_derived_blobs_state
    ON backup_asset_derived_blobs(state, updated_at);
CREATE INDEX idx_backup_asset_derived_blobs_kek
    ON backup_asset_derived_blobs(derived_kek_version, state);

CREATE TABLE backup_asset_derived_artifact_sets (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32),
    work_key TEXT NOT NULL CHECK (length(work_key) = 64 AND work_key NOT GLOB '*[^0-9a-f]*'),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    security_policy_revision TEXT NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 128),
    manifest_digest TEXT NOT NULL UNIQUE CHECK (length(manifest_digest) = 64 AND manifest_digest NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('active', 'stale', 'unavailable', 'superseded', 'revoked', 'purging', 'purge_failed')),
    revocation_reason TEXT NOT NULL DEFAULT '' CHECK (revocation_reason IN ('', 'explicit', 'expired', 'source_changed', 'policy_changed', 'key_loss', 'rollback')),
    completeness TEXT NOT NULL CHECK (completeness IN ('complete', 'partial')),
    artifact_count INTEGER NOT NULL CHECK (artifact_count > 0 AND artifact_count <= 256),
    total_plaintext_bytes INTEGER NOT NULL CHECK (total_plaintext_bytes >= 0),
    projection_required INTEGER NOT NULL DEFAULT 0 CHECK (projection_required IN (0, 1)),
    projection_published INTEGER NOT NULL DEFAULT 0 CHECK (projection_published IN (0, 1)),
    projection_revision INTEGER NOT NULL DEFAULT 0 CHECK (projection_revision >= 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    revoked_at DATETIME,
    CHECK (projection_published = 0 OR projection_required = 1),
    CHECK ((state IN ('active', 'stale') AND revocation_reason = '' AND revoked_at IS NULL)
        OR (state IN ('unavailable', 'superseded', 'revoked', 'purging', 'purge_failed') AND revocation_reason <> '' AND revoked_at IS NOT NULL)),
    UNIQUE (job_id),
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
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    artifact_set_id TEXT NOT NULL CHECK (length(artifact_set_id) = 32),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 256),
    role TEXT NOT NULL CHECK (role IN ('noop', 'content', 'ocr', 'thumbnail', 'metadata')),
    media_type TEXT NOT NULL CHECK (length(media_type) BETWEEN 1 AND 128),
    plaintext_size INTEGER NOT NULL CHECK (plaintext_size >= 0),
    plaintext_digest TEXT NOT NULL CHECK (length(plaintext_digest) = 64 AND plaintext_digest NOT GLOB '*[^0-9a-f]*'),
    completeness TEXT NOT NULL CHECK (completeness IN ('complete', 'partial')),
    coverage_canonical BLOB NOT NULL CHECK (length(coverage_canonical) BETWEEN 1 AND 4096),
    blob_id TEXT NOT NULL CHECK (length(blob_id) = 32),
    excerpt_ref TEXT NOT NULL DEFAULT '' CHECK (length(excerpt_ref) <= 128),
    created_at DATETIME NOT NULL,
    UNIQUE (artifact_set_id, ordinal),
    FOREIGN KEY (artifact_set_id) REFERENCES backup_asset_derived_artifact_sets(id) ON DELETE RESTRICT,
    FOREIGN KEY (blob_id) REFERENCES backup_asset_derived_blobs(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_derived_artifacts_blob
    ON backup_asset_derived_artifacts(blob_id, artifact_set_id);

CREATE TABLE backup_asset_derived_blob_references (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    blob_id TEXT NOT NULL CHECK (length(blob_id) = 32),
    artifact_id TEXT NOT NULL CHECK (length(artifact_id) = 32),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    state TEXT NOT NULL CHECK (state IN ('active', 'unavailable', 'revoked')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    revoked_at DATETIME,
    CHECK ((state = 'active' AND revoked_at IS NULL) OR (state IN ('unavailable', 'revoked') AND revoked_at IS NOT NULL)),
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
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('builtin', 'admin_registered')),
    source_id TEXT NOT NULL CHECK (length(source_id) BETWEEN 1 AND 128),
    version TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    manifest_digest TEXT NOT NULL CHECK (length(manifest_digest) = 64 AND manifest_digest NOT GLOB '*[^0-9a-f]*'),
    signing_key_fingerprint TEXT NOT NULL CHECK (length(signing_key_fingerprint) = 64 AND signing_key_fingerprint NOT GLOB '*[^0-9a-f]*'),
    bundle_fingerprint TEXT NOT NULL CHECK (length(bundle_fingerprint) = 64 AND bundle_fingerprint NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('registered', 'verified', 'active', 'superseded', 'failed')),
    failure_code TEXT NOT NULL DEFAULT '' CHECK (failure_code IN ('', 'invalid_signature', 'unsupported_version', 'policy_rejected', 'activation_failed')),
    verified_at DATETIME,
    activated_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK ((state = 'registered' AND verified_at IS NULL AND activated_at IS NULL AND failure_code = '')
        OR (state = 'verified' AND verified_at IS NOT NULL AND activated_at IS NULL AND failure_code = '')
        OR (state = 'active' AND verified_at IS NOT NULL AND activated_at IS NOT NULL AND failure_code = '')
        OR (state = 'superseded' AND verified_at IS NOT NULL AND failure_code = '')
        OR (state = 'failed' AND failure_code <> '')),
    UNIQUE (source_kind, source_id, version)
);

CREATE INDEX idx_backup_asset_updater_metadata_state
    ON backup_asset_updater_metadata(state, updated_at);
