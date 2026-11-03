BEGIN;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_export_items WHERE entry_fingerprint = '') THEN
        RAISE EXCEPTION '000097 downgrade blocked: empty export entry fingerprint exists';
    END IF;
END;
$$;
DROP TRIGGER trg_export_raw_fingerprint_downgrade_admission ON schema_migrations;
DROP FUNCTION export_raw_fingerprint_downgrade_admission();
ALTER TABLE backup_asset_export_items DROP CONSTRAINT backup_asset_export_items_entry_fingerprint_check;
ALTER TABLE backup_asset_export_items ADD CONSTRAINT backup_asset_export_items_entry_fingerprint_check
    CHECK (length(entry_fingerprint) BETWEEN 1 AND 128);
COMMIT;
