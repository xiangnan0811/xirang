BEGIN;

CREATE TABLE backup_asset_delivery_grants (
    id VARCHAR(32) PRIMARY KEY,
    delivery_id VARCHAR(32) NOT NULL UNIQUE,
    resource_kind VARCHAR(32) NOT NULL,
    recovery_point_id VARCHAR(32),
    catalog_generation_id VARCHAR(32),
    entry_id VARCHAR(64),
    recovery_job_id VARCHAR(32),
    recovery_result_id VARCHAR(32),
    owner_user_id BIGINT NOT NULL,
    session_jti VARCHAR(32) NOT NULL,
    session_token_version BIGINT NOT NULL,
    session_role VARCHAR(16) NOT NULL,
    session_expires_at TIMESTAMPTZ NOT NULL,
    action VARCHAR(16) NOT NULL,
    method_policy VARCHAR(16) NOT NULL,
    range_policy VARCHAR(16) NOT NULL,
    renderer VARCHAR(32) NOT NULL,
    profile VARCHAR(32) NOT NULL,
    classification VARCHAR(16) NOT NULL,
    classification_revision INTEGER NOT NULL,
    classification_source_revision BIGINT NOT NULL,
    step_up_action VARCHAR(64),
    step_up_proof_id VARCHAR(32),
    step_up_expires_at TIMESTAMPTZ,
    provider_kind VARCHAR(16) NOT NULL,
    source_fingerprint VARCHAR(128) NOT NULL,
    entry_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    fingerprint_strength VARCHAR(16) NOT NULL,
    representation_etag VARCHAR(160) NOT NULL,
    source_size BIGINT NOT NULL,
    source_modified_at TIMESTAMPTZ,
    detected_media_type VARCHAR(128) NOT NULL,
    representation_source_bytes BIGINT NOT NULL,
    representation_size BIGINT NOT NULL,
    representation_truncated BOOLEAN NOT NULL,
    cookie_secret_hash VARCHAR(64) NOT NULL,
    state VARCHAR(16) NOT NULL,
    revocation_reason VARCHAR(64) NOT NULL DEFAULT '',
    revoked_at TIMESTAMPTZ,
    lease_id VARCHAR(32) NOT NULL UNIQUE,
    lease_attempt_id VARCHAR(32) NOT NULL,
    lease_fence_token_hash VARCHAR(64) NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    idle_expires_at TIMESTAMPTZ NOT NULL,
    idle_ttl_seconds BIGINT NOT NULL,
    last_activity_at TIMESTAMPTZ NOT NULL,
    max_bytes_per_request BIGINT NOT NULL,
    max_cumulative_bytes BIGINT NOT NULL,
    max_requests BIGINT NOT NULL,
    max_in_flight BIGINT NOT NULL,
    reserved_bytes BIGINT NOT NULL DEFAULT 0,
    delivered_bytes BIGINT NOT NULL DEFAULT 0,
    request_count BIGINT NOT NULL DEFAULT 0,
    in_flight BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1,
    audit_state VARCHAR(16) NOT NULL DEFAULT 'none',
    audit_range_count BIGINT NOT NULL DEFAULT 0,
    audit_range_bytes BIGINT NOT NULL DEFAULT 0,
    audit_request_count BIGINT NOT NULL DEFAULT 0,
    audit_success_count BIGINT NOT NULL DEFAULT 0,
    audit_blocked_count BIGINT NOT NULL DEFAULT 0,
    audit_failure_count BIGINT NOT NULL DEFAULT 0,
    audit_failure_code VARCHAR(64) NOT NULL DEFAULT '',
    audit_attempt_count BIGINT NOT NULL DEFAULT 0,
    audit_next_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT backup_asset_delivery_grants_id_check CHECK (id ~ '^[0-9a-f]{32}$'),
    CONSTRAINT backup_asset_delivery_grants_delivery_id_check CHECK (delivery_id ~ '^[0-9a-f]{32}$'),
    CONSTRAINT backup_asset_delivery_grants_resource_check CHECK (
        resource_kind = 'backup_asset'
        AND recovery_point_id IS NOT NULL
        AND catalog_generation_id IS NOT NULL
        AND entry_id IS NOT NULL
        AND recovery_job_id IS NULL
        AND recovery_result_id IS NULL
    ),
    CONSTRAINT backup_asset_delivery_grants_session_check CHECK (
        session_jti ~ '^[0-9a-f]{32}$'
        AND session_token_version >= 0
        AND session_role IN ('admin', 'operator')
    ),
    CONSTRAINT backup_asset_delivery_grants_action_check CHECK (action IN ('preview', 'download')),
    CONSTRAINT backup_asset_delivery_grants_method_check CHECK (method_policy = 'get_head'),
    CONSTRAINT backup_asset_delivery_grants_range_check CHECK (range_policy IN ('none', 'single')),
    CONSTRAINT backup_asset_delivery_grants_renderer_check CHECK (renderer IN ('escaped_text', 'safe_raster', 'same_origin_pdf', 'native_audio', 'native_video', 'metadata_hex', 'attachment')),
    CONSTRAINT backup_asset_delivery_grants_profile_check CHECK (profile IN ('text_v1', 'raster_v1', 'pdf_v1', 'audio_v1', 'video_v1', 'hex_v1', 'original_v1')),
    CONSTRAINT backup_asset_delivery_grants_classification_check CHECK (
        classification IN ('non_secret', 'secret', 'unknown')
        AND classification_revision > 0
        AND classification_source_revision > 0
    ),
    CONSTRAINT backup_asset_delivery_grants_provider_check CHECK (provider_kind IN ('restic', 'rsync', 'rclone')),
    CONSTRAINT backup_asset_delivery_grants_source_check CHECK (
        char_length(source_fingerprint) BETWEEN 1 AND 128
        AND char_length(entry_fingerprint) <= 128
        AND fingerprint_strength IN ('strong', 'weak', 'none')
        AND char_length(representation_etag) BETWEEN 1 AND 160
        AND source_size >= 0
        AND char_length(detected_media_type) BETWEEN 1 AND 128
        AND representation_source_bytes >= 0
        AND representation_source_bytes <= source_size
        AND representation_size >= 0
    ),
    CONSTRAINT backup_asset_delivery_grants_secret_hash_check CHECK (cookie_secret_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT backup_asset_delivery_grants_state_check CHECK (state IN ('issued', 'active', 'draining', 'revoked', 'expired', 'closed')),
    CONSTRAINT backup_asset_delivery_grants_revocation_reason_check CHECK (revocation_reason IN ('', 'logout', 'session_revoked', 'session_changed', 'permission_changed', 'ownership_changed', 'classification_changed', 'point_unavailable', 'source_changed', 'lease_lost', 'expired', 'budget_exhausted', 'feature_disabled', 'shutdown', 'process_restarted', 'audit_failed', 'request_failed', 'cache_invalid')),
    CONSTRAINT backup_asset_delivery_grants_lease_check CHECK (
        lease_id ~ '^[0-9a-f]{32}$'
        AND lease_attempt_id ~ '^[0-9a-f]{32}$'
        AND lease_fence_token_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT backup_asset_delivery_grants_budget_check CHECK (
        idle_ttl_seconds > 0
        AND max_bytes_per_request > 0
        AND max_cumulative_bytes >= max_bytes_per_request
        AND max_requests > 0
        AND max_in_flight > 0
        AND reserved_bytes >= 0
        AND delivered_bytes >= 0
        AND request_count >= 0
        AND in_flight >= 0
        AND delivered_bytes <= max_cumulative_bytes
        AND reserved_bytes <= max_cumulative_bytes - delivered_bytes
        AND request_count <= max_requests
        AND in_flight <= max_in_flight
        AND version > 0
    ),
    CONSTRAINT backup_asset_delivery_grants_audit_check CHECK (
        audit_state IN ('none', 'pending', 'emitted', 'retry_wait', 'failed')
        AND audit_range_count >= 0
        AND audit_range_bytes >= 0
        AND audit_request_count >= 0
        AND audit_success_count >= 0
        AND audit_blocked_count >= 0
        AND audit_failure_count >= 0
        AND audit_failure_code IN ('', 'audit_write_failed', 'audit_backlog_full', 'reconciliation_failed')
        AND audit_attempt_count >= 0
    ),
    CONSTRAINT backup_asset_delivery_grants_renderer_product_check CHECK (
        (renderer = 'escaped_text' AND profile = 'text_v1' AND range_policy = 'none')
        OR (renderer = 'safe_raster' AND profile = 'raster_v1')
        OR (renderer = 'same_origin_pdf' AND profile = 'pdf_v1')
        OR (renderer = 'native_audio' AND profile = 'audio_v1')
        OR (renderer = 'native_video' AND profile = 'video_v1')
        OR (renderer = 'metadata_hex' AND profile = 'hex_v1' AND range_policy = 'none')
        OR (renderer = 'attachment' AND profile = 'original_v1')
    ),
    CONSTRAINT backup_asset_delivery_grants_representation_product_check CHECK (
        (renderer IN ('safe_raster', 'same_origin_pdf', 'native_audio', 'native_video', 'attachment')
            AND representation_source_bytes = source_size
            AND representation_size = source_size
            AND representation_truncated = FALSE)
        OR (renderer IN ('escaped_text', 'metadata_hex')
            AND ((representation_truncated = FALSE AND representation_source_bytes = source_size)
                OR (representation_truncated = TRUE AND representation_source_bytes < source_size)))
    ),
    CONSTRAINT backup_asset_delivery_grants_security_product_check CHECK (
        (action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.download' AND step_up_proof_id IS NOT NULL
            AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (action = 'preview' AND renderer <> 'attachment' AND classification = 'non_secret'
            AND step_up_action IS NULL AND step_up_proof_id IS NULL AND step_up_expires_at IS NULL)
        OR (action = 'preview' AND renderer <> 'attachment' AND classification IN ('secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.secret_reveal' AND step_up_proof_id IS NOT NULL
            AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
    ),
    CONSTRAINT backup_asset_delivery_grants_lifecycle_check CHECK (
        (state IN ('issued', 'active', 'closed') AND revocation_reason = '' AND revoked_at IS NULL)
        OR (state IN ('draining', 'revoked', 'expired') AND revocation_reason <> '' AND revoked_at IS NOT NULL)
    ),
    CONSTRAINT backup_asset_delivery_grants_expiry_check CHECK (
        last_activity_at <= idle_expires_at
        AND idle_expires_at <= absolute_expires_at
        AND absolute_expires_at <= session_expires_at
    ),
    CONSTRAINT backup_asset_delivery_grants_owner_fk FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT backup_asset_delivery_grants_catalog_fk FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT,
    CONSTRAINT backup_asset_delivery_grants_lease_fk FOREIGN KEY (lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT
);

CREATE INDEX idx_backup_asset_delivery_grants_delivery_state
    ON backup_asset_delivery_grants(delivery_id, state);
CREATE INDEX idx_backup_asset_delivery_grants_session_state
    ON backup_asset_delivery_grants(owner_user_id, session_jti, state);
CREATE INDEX idx_backup_asset_delivery_grants_resource_state
    ON backup_asset_delivery_grants(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_delivery_grants_expiry
    ON backup_asset_delivery_grants(state, idle_expires_at, absolute_expires_at);
CREATE INDEX idx_backup_asset_delivery_grants_audit
    ON backup_asset_delivery_grants(audit_state, audit_next_attempt_at, updated_at);

CREATE TABLE backup_asset_delivery_requests (
    id VARCHAR(32) PRIMARY KEY,
    grant_id VARCHAR(32) NOT NULL,
    method VARCHAR(8) NOT NULL,
    range_kind VARCHAR(16) NOT NULL,
    range_start BIGINT,
    range_end_exclusive BIGINT,
    suffix_length BIGINT,
    state VARCHAR(16) NOT NULL,
    reserved_bytes BIGINT NOT NULL DEFAULT 0,
    provider_bytes BIGINT NOT NULL DEFAULT 0,
    response_bytes BIGINT NOT NULL DEFAULT 0,
    http_status INTEGER NOT NULL DEFAULT 0,
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    last_progress_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT backup_asset_delivery_requests_id_check CHECK (id ~ '^[0-9a-f]{32}$'),
    CONSTRAINT backup_asset_delivery_requests_grant_id_check CHECK (grant_id ~ '^[0-9a-f]{32}$'),
    CONSTRAINT backup_asset_delivery_requests_method_check CHECK (method IN ('GET', 'HEAD')),
    CONSTRAINT backup_asset_delivery_requests_range_kind_check CHECK (range_kind IN ('full', 'normal', 'open_ended', 'suffix')),
    CONSTRAINT backup_asset_delivery_requests_state_check CHECK (state IN ('reserved', 'streaming', 'succeeded', 'blocked', 'canceled', 'failed', 'reconciled')),
    CONSTRAINT backup_asset_delivery_requests_counters_check CHECK (
        reserved_bytes >= 0
        AND provider_bytes >= 0 AND provider_bytes <= reserved_bytes
        AND response_bytes >= 0 AND response_bytes <= reserved_bytes
        AND (http_status = 0 OR http_status BETWEEN 100 AND 599)
        AND version > 0
    ),
    CONSTRAINT backup_asset_delivery_requests_failure_check CHECK (failure_code IN ('', 'invalid_range', 'range_not_allowed', 'if_range_full_forbidden', 'request_too_large', 'budget_exhausted', 'session_revoked', 'permission_changed', 'source_changed', 'lease_lost', 'feature_disabled', 'client_canceled', 'write_failed', 'source_failed', 'reconciled_crash', 'internal_failure')),
    CONSTRAINT backup_asset_delivery_requests_range_product_check CHECK (
        (range_kind = 'full' AND range_start IS NULL AND range_end_exclusive IS NULL AND suffix_length IS NULL)
        OR (range_kind = 'normal' AND range_start IS NOT NULL AND range_start >= 0 AND range_end_exclusive IS NOT NULL AND range_end_exclusive > range_start AND suffix_length IS NULL)
        OR (range_kind = 'open_ended' AND range_start IS NOT NULL AND range_start >= 0 AND range_end_exclusive IS NULL AND suffix_length IS NULL)
        OR (range_kind = 'suffix' AND range_start IS NULL AND range_end_exclusive IS NULL AND suffix_length IS NOT NULL AND suffix_length > 0)
    ),
    CONSTRAINT backup_asset_delivery_requests_head_check CHECK (method <> 'HEAD' OR reserved_bytes = 0),
    CONSTRAINT backup_asset_delivery_requests_time_check CHECK (last_progress_at >= started_at AND (finished_at IS NULL OR finished_at >= started_at)),
    CONSTRAINT backup_asset_delivery_requests_lifecycle_check CHECK (
        (state IN ('reserved', 'streaming') AND finished_at IS NULL AND http_status = 0 AND failure_code = '')
        OR (state = 'succeeded' AND finished_at IS NOT NULL AND http_status BETWEEN 200 AND 299 AND failure_code = '')
        OR (state IN ('blocked', 'canceled', 'failed', 'reconciled') AND finished_at IS NOT NULL AND http_status BETWEEN 100 AND 599 AND failure_code <> '')
    ),
    CONSTRAINT backup_asset_delivery_requests_grant_fk FOREIGN KEY (grant_id) REFERENCES backup_asset_delivery_grants(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_delivery_requests_grant_state
    ON backup_asset_delivery_requests(grant_id, state, started_at);
CREATE INDEX idx_backup_asset_delivery_requests_reconcile
    ON backup_asset_delivery_requests(state, last_progress_at);

CREATE TABLE backup_asset_delivery_usage (
    scope_kind VARCHAR(16) NOT NULL,
    scope_id VARCHAR(32) NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL,
    window_expires_at TIMESTAMPTZ NOT NULL,
    request_count BIGINT NOT NULL DEFAULT 0,
    reserved_bytes BIGINT NOT NULL DEFAULT 0,
    delivered_bytes BIGINT NOT NULL DEFAULT 0,
    in_flight BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope_kind, scope_id),
    CONSTRAINT backup_asset_delivery_usage_scope_kind_check CHECK (scope_kind IN ('global', 'user', 'provider')),
    CONSTRAINT backup_asset_delivery_usage_window_check CHECK (window_expires_at > window_started_at),
    CONSTRAINT backup_asset_delivery_usage_counters_check CHECK (
        request_count >= 0 AND reserved_bytes >= 0 AND delivered_bytes >= 0 AND in_flight >= 0 AND version > 0
    ),
    CONSTRAINT backup_asset_delivery_usage_scope_product_check CHECK (
        (scope_kind = 'global' AND scope_id = 'global')
        OR (scope_kind = 'provider' AND scope_id IN ('restic', 'rsync', 'rclone'))
        OR (scope_kind = 'user' AND CASE
            WHEN scope_id ~ '^[1-9][0-9]{0,18}$'
            THEN scope_id::NUMERIC <= 9223372036854775807
            ELSE FALSE
        END)
    )
);

CREATE INDEX idx_backup_asset_delivery_usage_window
    ON backup_asset_delivery_usage(window_expires_at, scope_kind, scope_id);

CREATE UNIQUE INDEX idx_backup_asset_audit_events_content_grant_action
    ON backup_asset_audit_events(grant_id, action)
    WHERE grant_id <> '' AND action IN ('preview_ticket', 'preview_read', 'asset_download_ticket', 'asset_download');

COMMIT;
