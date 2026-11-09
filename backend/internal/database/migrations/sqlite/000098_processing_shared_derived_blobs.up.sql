CREATE TEMP TABLE processing_000098_up_guard (valid INTEGER NOT NULL CHECK (valid = 1));
INSERT INTO processing_000098_up_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM sqlite_master
    WHERE tbl_name IN ('backup_asset_processing_uploads', 'backup_asset_derived_artifact_sets', 'backup_asset_derived_artifacts', 'backup_asset_derived_blob_references')
    AND (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND NOT (
        (tbl_name = 'backup_asset_processing_uploads' AND name = 'idx_backup_asset_processing_uploads_staging' AND lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) = 'createindexidx_backup_asset_processing_uploads_stagingonbackup_asset_processing_uploads(state,updated_at)') OR
        (tbl_name = 'backup_asset_derived_artifact_sets' AND name = 'idx_backup_asset_derived_sets_source_state' AND lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) = 'createindexidx_backup_asset_derived_sets_source_stateonbackup_asset_derived_artifact_sets(recovery_point_id,catalog_generation_id,entry_id,state)') OR
        (tbl_name = 'backup_asset_derived_artifact_sets' AND name = 'idx_backup_asset_derived_sets_work_state' AND lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) = 'createindexidx_backup_asset_derived_sets_work_stateonbackup_asset_derived_artifact_sets(work_key,state)') OR
        (tbl_name = 'backup_asset_derived_artifacts' AND name = 'idx_backup_asset_derived_artifacts_blob' AND lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) = 'createindexidx_backup_asset_derived_artifacts_blobonbackup_asset_derived_artifacts(blob_id,artifact_set_id)') OR
        (tbl_name = 'backup_asset_derived_blob_references' AND name = 'idx_backup_asset_derived_refs_source' AND lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) = 'createindexidx_backup_asset_derived_refs_sourceonbackup_asset_derived_blob_references(recovery_point_id,catalog_generation_id,entry_id,state)') OR
        (tbl_name = 'backup_asset_derived_blob_references' AND name = 'idx_backup_asset_derived_refs_blob_state' AND lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) = 'createindexidx_backup_asset_derived_refs_blob_stateonbackup_asset_derived_blob_references(blob_id,state)')
    )))) OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_processing_uploads', 'backup_asset_derived_artifact_sets', 'backup_asset_derived_artifacts', 'backup_asset_derived_blob_references')
        AND NOT ((source.name = 'backup_asset_derived_artifacts' AND ref."table" = 'backup_asset_derived_artifact_sets' AND ref."from" = 'artifact_set_id' AND ref."to" = 'id') OR
                 (source.name = 'backup_asset_derived_blob_references' AND ref."table" = 'backup_asset_derived_artifacts' AND ref."from" = 'artifact_id' AND ref."to" = 'id')))
    THEN 0 ELSE 1 END;
DROP TABLE processing_000098_up_guard;

CREATE TABLE backup_asset_processing_uploads_new (
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
    FOREIGN KEY (job_id) REFERENCES backup_asset_processing_jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (attempt_id) REFERENCES backup_asset_processing_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (grant_id) REFERENCES backup_asset_processing_grants(id) ON DELETE RESTRICT
);
INSERT INTO backup_asset_processing_uploads_new SELECT * FROM backup_asset_processing_uploads;
DROP TABLE backup_asset_processing_uploads;
ALTER TABLE backup_asset_processing_uploads_new RENAME TO backup_asset_processing_uploads;
CREATE INDEX idx_backup_asset_processing_uploads_staging ON backup_asset_processing_uploads(state, updated_at);
CREATE INDEX idx_backup_asset_processing_uploads_staging_id ON backup_asset_processing_uploads(staging_id, state);

CREATE TEMP TABLE processing_000098_references AS SELECT * FROM backup_asset_derived_blob_references;
CREATE TEMP TABLE processing_000098_artifacts AS SELECT * FROM backup_asset_derived_artifacts;
DROP TABLE backup_asset_derived_blob_references;
DROP TABLE backup_asset_derived_artifacts;
CREATE TABLE backup_asset_derived_artifact_sets_new (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 32),
    work_key TEXT NOT NULL CHECK (length(work_key) = 64 AND work_key NOT GLOB '*[^0-9a-f]*'),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) BETWEEN 1 AND 64),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    security_policy_revision TEXT NOT NULL CHECK (length(security_policy_revision) BETWEEN 1 AND 128),
    manifest_digest TEXT NOT NULL CHECK (length(manifest_digest) = 64 AND manifest_digest NOT GLOB '*[^0-9a-f]*'),
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
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id) REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);
INSERT INTO backup_asset_derived_artifact_sets_new SELECT * FROM backup_asset_derived_artifact_sets;
DROP TABLE backup_asset_derived_artifact_sets;
ALTER TABLE backup_asset_derived_artifact_sets_new RENAME TO backup_asset_derived_artifact_sets;
CREATE INDEX idx_backup_asset_derived_sets_source_state ON backup_asset_derived_artifact_sets(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_derived_sets_work_state ON backup_asset_derived_artifact_sets(work_key, state);
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
INSERT INTO backup_asset_derived_artifacts SELECT * FROM processing_000098_artifacts;
DROP TABLE processing_000098_artifacts;
CREATE INDEX idx_backup_asset_derived_artifacts_blob ON backup_asset_derived_artifacts(blob_id, artifact_set_id);
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
    FOREIGN KEY (catalog_generation_id, entry_id, recovery_point_id) REFERENCES catalog_entries(generation_id, entry_id, recovery_point_id) ON DELETE RESTRICT
);
INSERT INTO backup_asset_derived_blob_references SELECT * FROM processing_000098_references;
DROP TABLE processing_000098_references;
CREATE INDEX idx_backup_asset_derived_refs_source ON backup_asset_derived_blob_references(recovery_point_id, catalog_generation_id, entry_id, state);
CREATE INDEX idx_backup_asset_derived_refs_blob_state ON backup_asset_derived_blob_references(blob_id, state);

CREATE TRIGGER trg_processing_shared_derived_blobs_downgrade_admission BEFORE INSERT ON schema_migrations
WHEN NEW.version < 98 AND (
    EXISTS (SELECT 1 FROM backup_asset_processing_uploads GROUP BY staging_id HAVING COUNT(*) > 1)
    OR EXISTS (SELECT 1 FROM backup_asset_derived_artifact_sets GROUP BY manifest_digest HAVING COUNT(*) > 1)
    OR EXISTS (SELECT 1 FROM sqlite_master WHERE tbl_name IN ('backup_asset_processing_uploads', 'backup_asset_derived_artifact_sets', 'backup_asset_derived_artifacts', 'backup_asset_derived_blob_references') AND
        (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND (tbl_name, name, lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), ''))) NOT IN (
            ('backup_asset_processing_uploads', 'idx_backup_asset_processing_uploads_staging', 'createindexidx_backup_asset_processing_uploads_stagingonbackup_asset_processing_uploads(state,updated_at)'),
            ('backup_asset_processing_uploads', 'idx_backup_asset_processing_uploads_staging_id', 'createindexidx_backup_asset_processing_uploads_staging_idonbackup_asset_processing_uploads(staging_id,state)'),
            ('backup_asset_derived_artifact_sets', 'idx_backup_asset_derived_sets_source_state', 'createindexidx_backup_asset_derived_sets_source_stateonbackup_asset_derived_artifact_sets(recovery_point_id,catalog_generation_id,entry_id,state)'),
            ('backup_asset_derived_artifact_sets', 'idx_backup_asset_derived_sets_work_state', 'createindexidx_backup_asset_derived_sets_work_stateonbackup_asset_derived_artifact_sets(work_key,state)'),
            ('backup_asset_derived_artifacts', 'idx_backup_asset_derived_artifacts_blob', 'createindexidx_backup_asset_derived_artifacts_blobonbackup_asset_derived_artifacts(blob_id,artifact_set_id)'),
            ('backup_asset_derived_blob_references', 'idx_backup_asset_derived_refs_source', 'createindexidx_backup_asset_derived_refs_sourceonbackup_asset_derived_blob_references(recovery_point_id,catalog_generation_id,entry_id,state)'),
            ('backup_asset_derived_blob_references', 'idx_backup_asset_derived_refs_blob_state', 'createindexidx_backup_asset_derived_refs_blob_stateonbackup_asset_derived_blob_references(blob_id,state)')))))
    OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_processing_uploads', 'backup_asset_derived_artifact_sets', 'backup_asset_derived_artifacts', 'backup_asset_derived_blob_references')
        AND NOT ((source.name = 'backup_asset_derived_artifacts' AND ref."table" = 'backup_asset_derived_artifact_sets' AND ref."from" = 'artifact_set_id' AND ref."to" = 'id') OR
                 (source.name = 'backup_asset_derived_blob_references' AND ref."table" = 'backup_asset_derived_artifacts' AND ref."from" = 'artifact_id' AND ref."to" = 'id')))
)
BEGIN
    SELECT RAISE(ABORT, '000098 downgrade blocked: shared derived staging_id, manifest_digest, or extra schema objects exist');
END;
CREATE TRIGGER trg_processing_shared_derived_blobs_downgrade_update_admission BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 98 AND (
    EXISTS (SELECT 1 FROM backup_asset_processing_uploads GROUP BY staging_id HAVING COUNT(*) > 1)
    OR EXISTS (SELECT 1 FROM backup_asset_derived_artifact_sets GROUP BY manifest_digest HAVING COUNT(*) > 1)
    OR EXISTS (SELECT 1 FROM sqlite_master WHERE tbl_name IN ('backup_asset_processing_uploads', 'backup_asset_derived_artifact_sets', 'backup_asset_derived_artifacts', 'backup_asset_derived_blob_references') AND
        (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND (tbl_name, name, lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), ''))) NOT IN (
            ('backup_asset_processing_uploads', 'idx_backup_asset_processing_uploads_staging', 'createindexidx_backup_asset_processing_uploads_stagingonbackup_asset_processing_uploads(state,updated_at)'),
            ('backup_asset_processing_uploads', 'idx_backup_asset_processing_uploads_staging_id', 'createindexidx_backup_asset_processing_uploads_staging_idonbackup_asset_processing_uploads(staging_id,state)'),
            ('backup_asset_derived_artifact_sets', 'idx_backup_asset_derived_sets_source_state', 'createindexidx_backup_asset_derived_sets_source_stateonbackup_asset_derived_artifact_sets(recovery_point_id,catalog_generation_id,entry_id,state)'),
            ('backup_asset_derived_artifact_sets', 'idx_backup_asset_derived_sets_work_state', 'createindexidx_backup_asset_derived_sets_work_stateonbackup_asset_derived_artifact_sets(work_key,state)'),
            ('backup_asset_derived_artifacts', 'idx_backup_asset_derived_artifacts_blob', 'createindexidx_backup_asset_derived_artifacts_blobonbackup_asset_derived_artifacts(blob_id,artifact_set_id)'),
            ('backup_asset_derived_blob_references', 'idx_backup_asset_derived_refs_source', 'createindexidx_backup_asset_derived_refs_sourceonbackup_asset_derived_blob_references(recovery_point_id,catalog_generation_id,entry_id,state)'),
            ('backup_asset_derived_blob_references', 'idx_backup_asset_derived_refs_blob_state', 'createindexidx_backup_asset_derived_refs_blob_stateonbackup_asset_derived_blob_references(blob_id,state)')))))
    OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_processing_uploads', 'backup_asset_derived_artifact_sets', 'backup_asset_derived_artifacts', 'backup_asset_derived_blob_references')
        AND NOT ((source.name = 'backup_asset_derived_artifacts' AND ref."table" = 'backup_asset_derived_artifact_sets' AND ref."from" = 'artifact_set_id' AND ref."to" = 'id') OR
                 (source.name = 'backup_asset_derived_blob_references' AND ref."table" = 'backup_asset_derived_artifacts' AND ref."from" = 'artifact_id' AND ref."to" = 'id')))
)
BEGIN
    SELECT RAISE(ABORT, '000098 downgrade blocked: shared derived staging_id, manifest_digest, or extra schema objects exist');
END;
