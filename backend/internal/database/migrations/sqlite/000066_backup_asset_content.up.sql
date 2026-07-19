CREATE TABLE backup_asset_delivery_grants (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    delivery_id TEXT NOT NULL UNIQUE CHECK (length(delivery_id) = 32 AND delivery_id NOT GLOB '*[^0-9a-f]*'),
    resource_kind TEXT NOT NULL CHECK (resource_kind = 'backup_asset'),
    recovery_point_id TEXT,
    catalog_generation_id TEXT,
    entry_id TEXT,
    recovery_job_id TEXT,
    recovery_result_id TEXT,
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
        resource_kind = 'backup_asset'
        AND recovery_point_id IS NOT NULL
        AND catalog_generation_id IS NOT NULL
        AND entry_id IS NOT NULL
        AND recovery_job_id IS NULL
        AND recovery_result_id IS NULL
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
            AND representation_source_bytes = source_size
            AND representation_size = source_size
            AND representation_truncated = 0)
        OR (renderer IN ('escaped_text', 'metadata_hex')
            AND ((representation_truncated = 0 AND representation_source_bytes = source_size)
                OR (representation_truncated = 1 AND representation_source_bytes < source_size)))
    ),
    CHECK (
        (action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.download' AND step_up_proof_id IS NOT NULL
            AND length(step_up_proof_id) = 32 AND step_up_proof_id NOT GLOB '*[^0-9a-f]*'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (action = 'preview' AND renderer <> 'attachment' AND classification = 'non_secret'
            AND step_up_action IS NULL AND step_up_proof_id IS NULL AND step_up_expires_at IS NULL)
        OR (action = 'preview' AND renderer <> 'attachment' AND classification IN ('secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.secret_reveal' AND step_up_proof_id IS NOT NULL
            AND length(step_up_proof_id) = 32 AND step_up_proof_id NOT GLOB '*[^0-9a-f]*'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
    ),
    CHECK (
        (state IN ('issued', 'active', 'closed') AND revocation_reason = '' AND revoked_at IS NULL)
        OR (state IN ('draining', 'revoked', 'expired') AND revocation_reason <> '' AND revoked_at IS NOT NULL)
    ),
    CHECK (last_activity_at <= idle_expires_at AND idle_expires_at <= absolute_expires_at AND absolute_expires_at <= session_expires_at),
    CHECK (delivered_bytes <= max_cumulative_bytes AND reserved_bytes <= max_cumulative_bytes - delivered_bytes),
    CHECK (request_count <= max_requests AND in_flight <= max_in_flight),
    CHECK (
        (audit_state = 'none'
            AND audit_range_count = 0 AND audit_range_bytes = 0 AND audit_request_count = 0
            AND audit_success_count = 0 AND audit_blocked_count = 0 AND audit_failure_count = 0
            AND audit_failure_code = '' AND audit_attempt_count = 0 AND audit_next_attempt_at IS NULL)
        OR (
            audit_state IN ('pending', 'emitted', 'retry_wait', 'failed')
            AND audit_request_count > 0
            AND audit_request_count <= request_count
            AND audit_range_count <= audit_request_count
            AND (audit_range_count > 0 OR audit_range_bytes = 0)
            AND audit_range_bytes <= delivered_bytes
            AND audit_success_count + audit_blocked_count + audit_failure_count = audit_request_count
            AND (
                (audit_state = 'pending'
                    AND audit_failure_code = '' AND audit_attempt_count = 0 AND audit_next_attempt_at IS NULL)
                OR (audit_state = 'emitted'
                    AND audit_failure_code = '' AND audit_next_attempt_at IS NULL)
                OR (audit_state IN ('retry_wait', 'failed')
                    AND audit_failure_code <> '' AND audit_attempt_count > 0 AND audit_next_attempt_at IS NOT NULL)
            )
        )
    ),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT,
    FOREIGN KEY (lease_id) REFERENCES recovery_point_leases(id) ON DELETE RESTRICT
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
    CHECK (
        (range_kind = 'full' AND range_start IS NULL AND range_end_exclusive IS NULL AND suffix_length IS NULL)
        OR (range_kind = 'normal' AND range_start IS NOT NULL AND range_start >= 0 AND range_end_exclusive IS NOT NULL AND range_end_exclusive > range_start AND suffix_length IS NULL)
        OR (range_kind = 'open_ended' AND range_start IS NOT NULL AND range_start >= 0 AND range_end_exclusive IS NULL AND suffix_length IS NULL)
        OR (range_kind = 'suffix' AND range_start IS NULL AND range_end_exclusive IS NULL AND suffix_length IS NOT NULL AND suffix_length > 0)
    ),
    CHECK (method <> 'HEAD' OR reserved_bytes = 0),
    CHECK (last_progress_at >= started_at AND (finished_at IS NULL OR finished_at >= started_at)),
    CHECK (
        (state IN ('reserved', 'streaming') AND finished_at IS NULL AND http_status = 0 AND failure_code = '')
        OR (state = 'succeeded' AND finished_at IS NOT NULL AND http_status BETWEEN 200 AND 299 AND failure_code = '')
        OR (state IN ('blocked', 'canceled', 'failed', 'reconciled') AND finished_at IS NOT NULL AND http_status BETWEEN 100 AND 599 AND failure_code <> '')
    ),
    FOREIGN KEY (grant_id) REFERENCES backup_asset_delivery_grants(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_delivery_requests_grant_state
    ON backup_asset_delivery_requests(grant_id, state, started_at);
CREATE INDEX idx_backup_asset_delivery_requests_reconcile
    ON backup_asset_delivery_requests(state, last_progress_at);

CREATE TABLE backup_asset_delivery_usage (
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('global', 'user', 'provider')),
    scope_id TEXT NOT NULL CHECK (length(scope_id) BETWEEN 1 AND 32),
    window_started_at DATETIME NOT NULL,
    window_expires_at DATETIME NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    delivered_bytes INTEGER NOT NULL DEFAULT 0 CHECK (delivered_bytes >= 0),
    in_flight INTEGER NOT NULL DEFAULT 0 CHECK (in_flight >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (scope_kind, scope_id),
    CHECK (window_expires_at > window_started_at),
    CHECK (
        (scope_kind = 'global' AND scope_id = 'global')
        OR (scope_kind = 'provider' AND scope_id IN ('restic', 'rsync', 'rclone'))
        OR (scope_kind = 'user'
            AND length(scope_id) BETWEEN 1 AND 19
            AND scope_id GLOB '[1-9]*'
            AND scope_id NOT GLOB '*[^0-9]*'
            AND (length(scope_id) < 19 OR scope_id <= '9223372036854775807'))
    )
);

CREATE INDEX idx_backup_asset_delivery_usage_window
    ON backup_asset_delivery_usage(window_expires_at, scope_kind, scope_id);

CREATE UNIQUE INDEX idx_backup_asset_audit_events_content_grant_action
    ON backup_asset_audit_events(grant_id, action)
    WHERE grant_id <> '' AND action IN ('preview_ticket', 'preview_read', 'asset_download_ticket', 'asset_download');
