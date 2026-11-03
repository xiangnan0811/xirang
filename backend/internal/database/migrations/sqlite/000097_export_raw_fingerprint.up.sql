-- The child has an ON DELETE CASCADE reference to items. Preserve it before
-- replacing the parent; neither table has migration-defined triggers.
CREATE TEMP TABLE export_000097_up_guard (valid INTEGER NOT NULL CHECK (valid = 1));
INSERT INTO export_000097_up_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM sqlite_master
    WHERE tbl_name IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
    AND (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND NOT
        (tbl_name = 'backup_asset_export_items' AND name = 'idx_backup_asset_export_items_job_state' AND
         lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) =
         'createindexidx_backup_asset_export_items_job_stateonbackup_asset_export_items(job_id,state,ordinal)'))))
    OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND NOT (source.name = 'backup_asset_export_item_attempts' AND ref."table" = 'backup_asset_export_items'
            AND ref."from" = 'item_id' AND ref."to" = 'id'))
    THEN 0 ELSE 1 END;
DROP TABLE export_000097_up_guard;

CREATE TEMP TABLE export_000097_item_attempts AS SELECT * FROM backup_asset_export_item_attempts;
DROP TABLE backup_asset_export_item_attempts;

CREATE TABLE backup_asset_export_items_new (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    job_id TEXT NOT NULL CHECK (length(job_id) = 32),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    recovery_point_id TEXT NOT NULL CHECK (length(recovery_point_id) = 32),
    entry_id TEXT NOT NULL CHECK (length(entry_id) = 64),
    catalog_generation_id TEXT NOT NULL CHECK (length(catalog_generation_id) = 32),
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) BETWEEN 1 AND 128),
    entry_fingerprint TEXT NOT NULL CHECK (length(entry_fingerprint) <= 128 AND
        (length(entry_fingerprint) > 0 OR (fingerprint_strength = 'none' AND
         entry_type IN ('directory', 'symlink', 'hardlink', 'special', 'unknown')))),
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
INSERT INTO backup_asset_export_items_new SELECT * FROM backup_asset_export_items;
DROP TABLE backup_asset_export_items;
ALTER TABLE backup_asset_export_items_new RENAME TO backup_asset_export_items;
CREATE INDEX idx_backup_asset_export_items_job_state ON backup_asset_export_items(job_id, state, ordinal);

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
INSERT INTO backup_asset_export_item_attempts SELECT * FROM export_000097_item_attempts;
DROP TABLE export_000097_item_attempts;

CREATE TRIGGER trg_export_raw_fingerprint_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 97 AND (
    EXISTS (SELECT 1 FROM backup_asset_export_items WHERE entry_fingerprint = '')
    OR EXISTS (SELECT 1 FROM sqlite_master WHERE tbl_name IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND NOT
            (tbl_name = 'backup_asset_export_items' AND name = 'idx_backup_asset_export_items_job_state' AND
             lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) =
             'createindexidx_backup_asset_export_items_job_stateonbackup_asset_export_items(job_id,state,ordinal)'))))
    OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND NOT (source.name = 'backup_asset_export_item_attempts' AND ref."table" = 'backup_asset_export_items'
            AND ref."from" = 'item_id' AND ref."to" = 'id'))
)
BEGIN
    SELECT RAISE(ABORT, '000097 downgrade blocked: export fingerprint or extra schema objects exist');
END;
CREATE TRIGGER trg_export_raw_fingerprint_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 97 AND (
    EXISTS (SELECT 1 FROM backup_asset_export_items WHERE entry_fingerprint = '')
    OR EXISTS (SELECT 1 FROM sqlite_master WHERE tbl_name IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND (type = 'trigger' OR (type = 'index' AND sql IS NOT NULL AND NOT
            (tbl_name = 'backup_asset_export_items' AND name = 'idx_backup_asset_export_items_job_state' AND
             lower(replace(replace(replace(replace(sql, ' ', ''), char(10), ''), char(13), ''), char(9), '')) =
             'createindexidx_backup_asset_export_items_job_stateonbackup_asset_export_items(job_id,state,ordinal)'))))
    OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref
        WHERE source.type = 'table' AND ref."table" IN ('backup_asset_export_items', 'backup_asset_export_item_attempts')
        AND NOT (source.name = 'backup_asset_export_item_attempts' AND ref."table" = 'backup_asset_export_items'
            AND ref."from" = 'item_id' AND ref."to" = 'id'))
)
BEGIN
    SELECT RAISE(ABORT, '000097 downgrade blocked: export fingerprint or extra schema objects exist');
END;
