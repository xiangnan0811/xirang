BEGIN;

ALTER TABLE backup_asset_export_items
    DROP CONSTRAINT backup_asset_export_items_entry_fingerprint_check;
ALTER TABLE backup_asset_export_items
    ADD CONSTRAINT backup_asset_export_items_entry_fingerprint_check CHECK (
        length(entry_fingerprint) <= 128 AND
        (length(entry_fingerprint) > 0 OR
         (fingerprint_strength = 'none' AND entry_type IN ('directory', 'symlink', 'hardlink', 'special', 'unknown')))
    );

CREATE FUNCTION export_raw_fingerprint_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 97 AND EXISTS (SELECT 1 FROM backup_asset_export_items WHERE entry_fingerprint = '') THEN
        RAISE EXCEPTION '000097 downgrade blocked: empty export entry fingerprint exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_export_raw_fingerprint_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION export_raw_fingerprint_downgrade_admission();

COMMIT;
