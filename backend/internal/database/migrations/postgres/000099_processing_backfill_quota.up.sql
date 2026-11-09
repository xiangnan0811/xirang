BEGIN;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM backup_asset_processing_jobs j
        WHERE (SELECT count(*) FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id
               WHERE p.id = j.recovery_point_id) <> 1
           OR NOT EXISTS (SELECT 1 FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id
               WHERE p.id = j.recovery_point_id AND lower(trim(r.provider_kind)) IN ('restic','rsync','rclone','command'))
           OR (SELECT count(*) FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id
               AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id) <> 1
           OR NOT EXISTS (SELECT 1 FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id
               AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id AND e.size >= 0)
    ) THEN
        RAISE EXCEPTION '000099 upgrade blocked: invalid processing source association';
    END IF;
END;
$$;
ALTER TABLE backup_asset_processing_jobs
    ADD COLUMN provider_kind VARCHAR(32) NOT NULL DEFAULT '' CHECK (provider_kind IN ('', 'restic', 'rsync', 'rclone', 'command')),
    ADD COLUMN estimated_input_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_input_bytes >= 0),
    ADD COLUMN backfill_charged BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE backup_asset_processing_jobs j SET
    provider_kind = (SELECT lower(trim(r.provider_kind)) FROM recovery_points p JOIN backup_repositories r ON r.id = p.repository_id WHERE p.id = j.recovery_point_id),
    estimated_input_bytes = (SELECT e.size FROM catalog_entries e WHERE e.generation_id = j.catalog_generation_id AND e.entry_id = j.entry_id AND e.recovery_point_id = j.recovery_point_id);
CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind, capability);
CREATE TABLE backup_asset_processing_backfill_ledger (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    window_started_at TIMESTAMPTZ NOT NULL,
    jobs_count BIGINT NOT NULL DEFAULT 0 CHECK (jobs_count >= 0),
    bytes_count BIGINT NOT NULL DEFAULT 0 CHECK (bytes_count >= 0),
    updated_at TIMESTAMPTZ NOT NULL
);
INSERT INTO backup_asset_processing_backfill_ledger VALUES (1, '1970-01-01 00:00:00+00', 0, 0, '1970-01-01 00:00:00+00');
CREATE FUNCTION processing_backfill_quota_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 99 THEN
        PERFORM 1 FROM backup_asset_processing_backfill_ledger WHERE id = 1 FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION '000099 downgrade blocked: ledger missing';
        END IF;
        IF EXISTS (SELECT 1 FROM backup_asset_processing_backfill_ledger WHERE jobs_count > 0 OR bytes_count > 0)
            OR EXISTS (SELECT 1 FROM backup_asset_processing_jobs WHERE backfill_charged) THEN
            RAISE EXCEPTION '000099 downgrade blocked: backfill quota used';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_processing_backfill_quota_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION processing_backfill_quota_downgrade_admission();
COMMIT;
