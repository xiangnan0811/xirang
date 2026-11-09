CREATE TEMP TABLE processing_000099_up_guard (valid INTEGER NOT NULL CHECK (valid = 1));
INSERT INTO processing_000099_up_guard
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM backup_asset_processing_jobs j
    WHERE (SELECT count(*) FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id
           WHERE p.id = j.recovery_point_id) <> 1
       OR NOT EXISTS (SELECT 1 FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id
           WHERE p.id = j.recovery_point_id AND lower(trim(r.provider_kind)) IN ('restic','rsync','rclone','command'))
       OR (SELECT count(*) FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id
           AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id) <> 1
       OR NOT EXISTS (SELECT 1 FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id
           AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id AND e.size >= 0)
) THEN 0 ELSE 1 END;
DROP TABLE processing_000099_up_guard;
ALTER TABLE backup_asset_processing_jobs ADD COLUMN provider_kind TEXT NOT NULL DEFAULT '' CHECK (provider_kind IN ('', 'restic', 'rsync', 'rclone', 'command'));
ALTER TABLE backup_asset_processing_jobs ADD COLUMN estimated_input_bytes INTEGER NOT NULL DEFAULT 0 CHECK (estimated_input_bytes >= 0);
ALTER TABLE backup_asset_processing_jobs ADD COLUMN backfill_charged INTEGER NOT NULL DEFAULT 0 CHECK (backfill_charged IN (0, 1));
UPDATE backup_asset_processing_jobs SET
    provider_kind = (SELECT lower(trim(r.provider_kind)) FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id WHERE p.id = backup_asset_processing_jobs.recovery_point_id),
    estimated_input_bytes = (SELECT e.size FROM catalog_entries e WHERE e.generation_id = backup_asset_processing_jobs.catalog_generation_id AND e.entry_id = backup_asset_processing_jobs.entry_id AND e.recovery_point_id = backup_asset_processing_jobs.recovery_point_id);
CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind, capability);
CREATE TABLE backup_asset_processing_backfill_ledger (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    window_started_at DATETIME NOT NULL,
    jobs_count INTEGER NOT NULL DEFAULT 0 CHECK (jobs_count >= 0),
    bytes_count INTEGER NOT NULL DEFAULT 0 CHECK (bytes_count >= 0),
    updated_at DATETIME NOT NULL
);
INSERT INTO backup_asset_processing_backfill_ledger VALUES (1, '1970-01-01 00:00:00Z', 0, 0, '1970-01-01 00:00:00Z');
CREATE VIEW processing_000099_downgrade_blocked AS
SELECT 1 WHERE (SELECT count(*) FROM backup_asset_processing_backfill_ledger WHERE id = 1) <> 1
 OR EXISTS (SELECT 1 FROM backup_asset_processing_backfill_ledger WHERE jobs_count > 0 OR bytes_count > 0)
 OR EXISTS (SELECT 1 FROM backup_asset_processing_jobs WHERE backfill_charged = 1)
 OR EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'trigger' AND (tbl_name IN ('backup_asset_processing_jobs','backup_asset_processing_backfill_ledger') OR lower(sql) LIKE '%backup_asset_processing_jobs%' OR lower(sql) LIKE '%backup_asset_processing_backfill_ledger%'))
 OR EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL AND tbl_name IN ('backup_asset_processing_jobs','backup_asset_processing_backfill_ledger') AND (name, lower(replace(replace(replace(replace(sql,' ',''),char(10),''),char(13),''),char(9),''))) NOT IN (
 ('idx_backup_asset_processing_jobs_current_work','createuniqueindexidx_backup_asset_processing_jobs_current_workonbackup_asset_processing_jobs(work_key)whereis_current=1'),
 ('idx_backup_asset_processing_jobs_queue','createindexidx_backup_asset_processing_jobs_queueonbackup_asset_processing_jobs(state,priority_class,effective_prioritydesc,queued_at,id)'),
 ('idx_backup_asset_processing_jobs_source','createindexidx_backup_asset_processing_jobs_sourceonbackup_asset_processing_jobs(recovery_point_id,catalog_generation_id,entry_id,state)'),
 ('idx_backup_asset_processing_jobs_retry','createindexidx_backup_asset_processing_jobs_retryonbackup_asset_processing_jobs(state,retry_at)'),
 ('idx_backup_asset_processing_jobs_backfill_active','createindexidx_backup_asset_processing_jobs_backfill_activeonbackup_asset_processing_jobs(is_current,priority_class,state,provider_kind,capability)')))
 OR (SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('idx_backup_asset_processing_jobs_current_work','idx_backup_asset_processing_jobs_queue','idx_backup_asset_processing_jobs_source','idx_backup_asset_processing_jobs_retry','idx_backup_asset_processing_jobs_backfill_active')) <> 5
 OR EXISTS (SELECT 1 FROM sqlite_master AS source JOIN pragma_foreign_key_list(source.name) AS ref WHERE source.type = 'table' AND (ref."table" = 'backup_asset_processing_backfill_ledger' OR (ref."table" = 'backup_asset_processing_jobs' AND ref."to" IN ('provider_kind','estimated_input_bytes','backfill_charged'))))
 OR EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'view' AND name <> 'processing_000099_downgrade_blocked' AND (lower(sql) LIKE '%backup_asset_processing_jobs%' OR lower(sql) LIKE '%backup_asset_processing_backfill_ledger%'));
CREATE TRIGGER trg_processing_backfill_quota_downgrade_admission BEFORE INSERT ON schema_migrations
WHEN NEW.version < 99 AND EXISTS (SELECT 1 FROM processing_000099_downgrade_blocked)
BEGIN
    SELECT RAISE(ABORT, '000099 downgrade blocked: backfill quota used or schema dependencies exist');
END;
CREATE TRIGGER trg_processing_backfill_quota_downgrade_update_admission BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 99 AND EXISTS (SELECT 1 FROM processing_000099_downgrade_blocked)
BEGIN
    SELECT RAISE(ABORT, '000099 downgrade blocked: backfill quota used or schema dependencies exist');
END;
