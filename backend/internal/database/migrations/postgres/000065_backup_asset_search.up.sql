BEGIN;

ALTER TABLE wrapped_domain_keys
    DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys
    ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token'));

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication', 'search_index'));

CREATE UNIQUE INDEX idx_catalog_generations_id_recovery_point
    ON catalog_generations(id, recovery_point_id);
CREATE UNIQUE INDEX idx_catalog_entries_generation_entry_recovery_point
    ON catalog_entries(generation_id, entry_id, recovery_point_id);

CREATE TABLE backup_asset_search_generations (
    id VARCHAR(32) PRIMARY KEY,
    recovery_point_id VARCHAR(32) NOT NULL,
    catalog_generation_id VARCHAR(32) NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    state VARCHAR(16) NOT NULL CHECK (state IN ('building', 'complete', 'failed', 'superseded')),
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    source_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    normalizer_version INTEGER NOT NULL CHECK (normalizer_version > 0),
    search_key_version INTEGER NOT NULL CHECK (search_key_version > 0),
    projection_revision BIGINT NOT NULL DEFAULT 1 CHECK (projection_revision > 0),
    lease_id VARCHAR(32) NOT NULL,
    build_attempt_id VARCHAR(32) NOT NULL,
    fence_token_hash VARCHAR(64) NOT NULL CHECK (char_length(fence_token_hash) = 64),
    expected_document_count BIGINT NOT NULL DEFAULT 0 CHECK (expected_document_count >= 0),
    written_document_count BIGINT NOT NULL DEFAULT 0 CHECK (written_document_count >= 0),
    error_code VARCHAR(64) NOT NULL DEFAULT '' CHECK (error_code IN ('', 'search_build_abandoned', 'search_build_failed', 'search_build_limit', 'search_build_timeout', 'search_catalog_changed', 'search_source_changed', 'search_fence_lost', 'search_key_unavailable', 'search_invalid_security_state', 'search_projection_mismatch')),
    correlation_id VARCHAR(64) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (NOT is_active OR state = 'complete'),
    UNIQUE (recovery_point_id, generation),
    UNIQUE (id, recovery_point_id, catalog_generation_id),
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE,
    FOREIGN KEY (catalog_generation_id, recovery_point_id)
        REFERENCES catalog_generations(id, recovery_point_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_backup_asset_search_generations_active
    ON backup_asset_search_generations(recovery_point_id)
    WHERE is_active = TRUE;
CREATE INDEX idx_backup_asset_search_generations_catalog_state
    ON backup_asset_search_generations(catalog_generation_id, state);
CREATE INDEX idx_backup_asset_search_generations_reconcile
    ON backup_asset_search_generations(state, updated_at);

CREATE TABLE backup_asset_search_documents (
    search_generation_id VARCHAR(32) NOT NULL,
    document_id VARCHAR(64) NOT NULL,
    recovery_point_id VARCHAR(32) NOT NULL,
    catalog_generation_id VARCHAR(32) NOT NULL,
    entry_id VARCHAR(64) NOT NULL,
    sensitivity VARCHAR(16) NOT NULL CHECK (sensitivity IN ('non_secret', 'secret', 'unknown')),
    classification_revision INTEGER NOT NULL CHECK (classification_revision > 0),
    metadata_revision INTEGER NOT NULL CHECK (metadata_revision > 0),
    entry_type VARCHAR(16) NOT NULL CHECK (entry_type IN ('file', 'directory', 'symlink', 'hardlink', 'special', 'unknown')),
    modified_at TIMESTAMPTZ,
    lineage_token VARCHAR(64) NOT NULL CHECK (char_length(lineage_token) = 64),
    path_group_token VARCHAR(64) NOT NULL CHECK (char_length(path_group_token) = 64),
    path_sort_key TEXT NOT NULL,
    name_sort_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (search_generation_id, document_id),
    UNIQUE (search_generation_id, entry_id),
    FOREIGN KEY (search_generation_id, recovery_point_id, catalog_generation_id)
        REFERENCES backup_asset_search_generations(id, recovery_point_id, catalog_generation_id) ON DELETE CASCADE,
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id)
        REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_search_documents_candidate
    ON backup_asset_search_documents(search_generation_id, sensitivity, entry_type, modified_at, document_id);
CREATE INDEX idx_backup_asset_search_documents_group
    ON backup_asset_search_documents(search_generation_id, lineage_token, path_group_token, document_id);

CREATE TABLE backup_asset_search_postings (
    search_generation_id VARCHAR(32) NOT NULL,
    document_id VARCHAR(64) NOT NULL,
    field VARCHAR(32) NOT NULL CHECK (field IN ('name', 'path', 'extension', 'type', 'modified_time', 'content', 'ocr')),
    token_kind VARCHAR(16) NOT NULL CHECK (token_kind IN ('exact', 'segment', 'bigram', 'date')),
    key_version INTEGER NOT NULL CHECK (key_version > 0),
    token_hmac VARCHAR(64) NOT NULL CHECK (char_length(token_hmac) = 64),
    term_frequency INTEGER NOT NULL CHECK (term_frequency > 0),
    FOREIGN KEY (search_generation_id, document_id)
        REFERENCES backup_asset_search_documents(search_generation_id, document_id) ON DELETE CASCADE,
    UNIQUE (search_generation_id, document_id, field, token_kind, token_hmac)
);

CREATE INDEX idx_backup_asset_search_postings_lookup
    ON backup_asset_search_postings(search_generation_id, field, token_kind, token_hmac, document_id);
CREATE INDEX idx_backup_asset_search_postings_document
    ON backup_asset_search_postings(search_generation_id, document_id);

CREATE TABLE backup_asset_search_document_fields (
    search_generation_id VARCHAR(32) NOT NULL,
    document_id VARCHAR(64) NOT NULL,
    field VARCHAR(32) NOT NULL CHECK (field IN ('name', 'path', 'extension', 'type', 'modified_time', 'content', 'ocr')),
    state VARCHAR(16) NOT NULL CHECK (state IN ('complete', 'partial', 'building', 'failed', 'unavailable')),
    coverage_revision INTEGER NOT NULL CHECK (coverage_revision > 0),
    classification_revision INTEGER NOT NULL CHECK (classification_revision > 0),
    pipeline_revision INTEGER NOT NULL CHECK (pipeline_revision > 0),
    index_revision INTEGER NOT NULL CHECK (index_revision > 0),
    source_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    excerpt_ref VARCHAR(32) CHECK (excerpt_ref IS NULL OR char_length(excerpt_ref) = 32),
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (search_generation_id, document_id)
        REFERENCES backup_asset_search_documents(search_generation_id, document_id) ON DELETE CASCADE,
    UNIQUE (search_generation_id, document_id, field)
);

CREATE INDEX idx_backup_asset_search_document_fields_coverage
    ON backup_asset_search_document_fields(search_generation_id, field, state, document_id);

CREATE TABLE backup_asset_saved_searches (
    id VARCHAR(32) PRIMARY KEY,
    owner_user_id BIGINT NOT NULL,
    encrypted_ast TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    scope_mode VARCHAR(16) NOT NULL CHECK (scope_mode IN ('current', 'all_retained', 'exact_points')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'broken', 'blocked')),
    state_reason VARCHAR(32) NOT NULL DEFAULT '' CHECK (state_reason IN ('', 'point_retired', 'point_expiring', 'point_expired', 'point_failed', 'point_purge_blocked', 'point_missing', 'scope_unauthorized', 'ast_schema_unsupported')),
    broken_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_backup_asset_saved_searches_owner_list
    ON backup_asset_saved_searches(owner_user_id, updated_at, id);
CREATE INDEX idx_backup_asset_saved_searches_owner_state
    ON backup_asset_saved_searches(owner_user_id, state);

CREATE TABLE backup_asset_saved_search_scope_points (
    saved_search_id VARCHAR(32) NOT NULL,
    recovery_point_id VARCHAR(32) NOT NULL,
    PRIMARY KEY (saved_search_id, recovery_point_id),
    FOREIGN KEY (saved_search_id) REFERENCES backup_asset_saved_searches(id) ON DELETE CASCADE
);

CREATE TABLE backup_asset_favorites (
    id VARCHAR(32) PRIMARY KEY,
    owner_user_id BIGINT NOT NULL,
    recovery_point_id VARCHAR(32) NOT NULL,
    entry_id VARCHAR(64) NOT NULL,
    encrypted_label TEXT NOT NULL DEFAULT '',
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'tombstone')),
    tombstone_reason VARCHAR(32) NOT NULL DEFAULT '' CHECK (tombstone_reason IN ('', 'source_retired', 'source_expiring', 'source_expired', 'source_failed', 'source_purge_blocked', 'source_missing')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    UNIQUE (owner_user_id, recovery_point_id, entry_id)
);

CREATE INDEX idx_backup_asset_favorites_owner_list
    ON backup_asset_favorites(owner_user_id, state, updated_at, id);

CREATE TABLE backup_asset_tag_definitions (
    id VARCHAR(32) PRIMARY KEY,
    owner_user_id BIGINT NOT NULL,
    encrypted_name TEXT NOT NULL,
    name_token VARCHAR(64) NOT NULL CHECK (char_length(name_token) = 64),
    key_version INTEGER NOT NULL CHECK (key_version > 0),
    token_state VARCHAR(16) NOT NULL CHECK (token_state IN ('active', 'rekeying')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    UNIQUE (owner_user_id, name_token),
    UNIQUE (id, owner_user_id)
);

CREATE INDEX idx_backup_asset_tag_definitions_owner_list
    ON backup_asset_tag_definitions(owner_user_id, token_state, updated_at, id);

CREATE TABLE backup_asset_tag_assignments (
    id VARCHAR(32) PRIMARY KEY,
    owner_user_id BIGINT NOT NULL,
    tag_id VARCHAR(32) NOT NULL,
    recovery_point_id VARCHAR(32) NOT NULL,
    entry_id VARCHAR(64) NOT NULL,
    state VARCHAR(16) NOT NULL CHECK (state IN ('active', 'tombstone')),
    tombstone_reason VARCHAR(32) NOT NULL DEFAULT '' CHECK (tombstone_reason IN ('', 'source_retired', 'source_expiring', 'source_expired', 'source_failed', 'source_purge_blocked', 'source_missing')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (tag_id, owner_user_id)
        REFERENCES backup_asset_tag_definitions(id, owner_user_id) ON DELETE CASCADE,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    UNIQUE (owner_user_id, tag_id, recovery_point_id, entry_id)
);

CREATE INDEX idx_backup_asset_tag_assignments_owner_target
    ON backup_asset_tag_assignments(owner_user_id, recovery_point_id, entry_id, state);
CREATE INDEX idx_backup_asset_tag_assignments_tag
    ON backup_asset_tag_assignments(owner_user_id, tag_id, state, updated_at);

CREATE TABLE backup_asset_recent_access (
    id VARCHAR(32) PRIMARY KEY,
    owner_user_id BIGINT NOT NULL,
    recovery_point_id VARCHAR(32) NOT NULL,
    entry_id VARCHAR(64) NOT NULL,
    access_count BIGINT NOT NULL CHECK (access_count > 0),
    last_accessed_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    UNIQUE (owner_user_id, recovery_point_id, entry_id)
);

CREATE INDEX idx_backup_asset_recent_access_owner_list
    ON backup_asset_recent_access(owner_user_id, last_accessed_at, id);
CREATE INDEX idx_backup_asset_recent_access_expiry
    ON backup_asset_recent_access(expires_at, id);

CREATE TABLE backup_asset_overlay_usage (
    owner_user_id BIGINT PRIMARY KEY,
    saved_search_count BIGINT NOT NULL DEFAULT 0 CHECK (saved_search_count >= 0),
    favorite_count BIGINT NOT NULL DEFAULT 0 CHECK (favorite_count >= 0),
    tag_definition_count BIGINT NOT NULL DEFAULT 0 CHECK (tag_definition_count >= 0),
    tag_assignment_count BIGINT NOT NULL DEFAULT 0 CHECK (tag_assignment_count >= 0),
    recent_count BIGINT NOT NULL DEFAULT 0 CHECK (recent_count >= 0),
    recent_rate_window_started_at TIMESTAMPTZ NOT NULL,
    recent_rate_window_write_count BIGINT NOT NULL DEFAULT 0 CHECK (recent_rate_window_write_count >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE backup_asset_overlay_idempotency (
    id VARCHAR(32) PRIMARY KEY,
    owner_user_id BIGINT NOT NULL,
    action VARCHAR(32) NOT NULL CHECK (action IN ('saved_search_create', 'saved_search_update', 'saved_search_delete', 'favorite_add', 'favorite_remove', 'tag_create', 'tag_update', 'tag_delete', 'tag_assign', 'tag_unassign', 'recent_clear')),
    key_hash VARCHAR(64) NOT NULL CHECK (char_length(key_hash) = 64),
    encrypted_request_fingerprint TEXT NOT NULL,
    result_resource_type VARCHAR(32) NOT NULL CHECK (result_resource_type IN ('none', 'saved_search', 'favorite', 'tag_definition', 'tag_assignment', 'recent')),
    result_resource_id VARCHAR(64) NOT NULL DEFAULT '',
    result_version INTEGER NOT NULL DEFAULT 0 CHECK (result_version >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    UNIQUE (owner_user_id, action, key_hash)
);

CREATE INDEX idx_backup_asset_overlay_idempotency_expiry
    ON backup_asset_overlay_idempotency(expires_at, id);

COMMIT;
