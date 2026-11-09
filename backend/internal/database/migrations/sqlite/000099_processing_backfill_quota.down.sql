CREATE TEMP TABLE processing_000099_down_guard (valid INTEGER NOT NULL CHECK (valid = 1));
INSERT INTO processing_000099_down_guard SELECT CASE WHEN
    (SELECT count(*) FROM backup_asset_processing_backfill_ledger WHERE id = 1) <> 1
    OR EXISTS (SELECT 1 FROM backup_asset_processing_backfill_ledger WHERE jobs_count > 0 OR bytes_count > 0)
    OR EXISTS (SELECT 1 FROM backup_asset_processing_jobs WHERE backfill_charged = 1)
    OR EXISTS (SELECT 1 FROM processing_000099_downgrade_blocked)
    THEN 0 ELSE 1 END;
DROP TABLE processing_000099_down_guard;
DROP TRIGGER trg_processing_backfill_quota_downgrade_admission;
DROP TRIGGER trg_processing_backfill_quota_downgrade_update_admission;
DROP VIEW processing_000099_downgrade_blocked;
DROP INDEX idx_backup_asset_processing_jobs_backfill_active;
ALTER TABLE backup_asset_processing_jobs DROP COLUMN backfill_charged;
ALTER TABLE backup_asset_processing_jobs DROP COLUMN estimated_input_bytes;
ALTER TABLE backup_asset_processing_jobs DROP COLUMN provider_kind;
DROP TABLE backup_asset_processing_backfill_ledger;
