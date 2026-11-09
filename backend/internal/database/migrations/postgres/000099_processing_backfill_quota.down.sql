BEGIN;
LOCK TABLE backup_asset_processing_jobs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE schema_migrations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE backup_asset_processing_backfill_ledger IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
    IF (SELECT count(*) FROM backup_asset_processing_backfill_ledger WHERE id = 1) <> 1
        OR EXISTS (SELECT 1 FROM backup_asset_processing_backfill_ledger WHERE jobs_count > 0 OR bytes_count > 0)
        OR EXISTS (SELECT 1 FROM backup_asset_processing_jobs WHERE backfill_charged) THEN
        RAISE EXCEPTION '000099 downgrade blocked: backfill quota used or ledger missing';
    END IF;
END;
$$;
DROP TRIGGER trg_processing_backfill_quota_downgrade_admission ON schema_migrations;
DROP FUNCTION processing_backfill_quota_downgrade_admission();
DROP INDEX idx_backup_asset_processing_jobs_backfill_active;
ALTER TABLE backup_asset_processing_jobs DROP COLUMN backfill_charged, DROP COLUMN estimated_input_bytes, DROP COLUMN provider_kind;
DROP TABLE backup_asset_processing_backfill_ledger;
COMMIT;
