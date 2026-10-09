BEGIN;

-- The cron-backup alert identity is a fixed prefix plus a 64-character source
-- key. Widen PostgreSQL before the worker can persist that exact identity.
ALTER TABLE alerts ALTER COLUMN error_code TYPE VARCHAR(128);

-- 000092 stores one durable cron-backup fault cursor per state source.
-- The usage marker is written with the first enrollment and survives cursor
-- cleanup, so a downgrade cannot erase evidence that the schema was used.
CREATE TABLE cron_backup_health (
    source_key TEXT NOT NULL PRIMARY KEY
        CHECK (length(source_key) = 64 AND source_key ~ '^[0-9a-f]{64}$'),
    source_id TEXT NOT NULL DEFAULT ''
        CHECK (source_id = '' OR source_id ~ '^[0-9a-f]{32}$'),
    highest_revision BIGINT NOT NULL DEFAULT 0
        CHECK (highest_revision >= 0),
    enrolled_at TIMESTAMPTZ NOT NULL,
    fault_active BOOLEAN NOT NULL DEFAULT FALSE,
    alert_id INTEGER,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT cron_backup_health_fault_alert_check
        CHECK (NOT fault_active OR alert_id IS NOT NULL),
    CONSTRAINT cron_backup_health_alert_fk
        FOREIGN KEY (alert_id) REFERENCES alerts(id) ON DELETE RESTRICT
);

CREATE TABLE cron_backup_health_usage (
    id INTEGER NOT NULL PRIMARY KEY
        CHECK (id = 1)
);

-- golang-migrate writes the target version before running a down migration.
-- Reject a used 000092 downgrade, or one that would truncate the exact
-- cron-backup alert identity, at that metadata boundary.
CREATE OR REPLACE FUNCTION cron_backup_health_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 92 AND (
        EXISTS (SELECT 1 FROM cron_backup_health_usage)
        OR EXISTS (SELECT 1 FROM alerts WHERE length(error_code) > 64)
    ) THEN
        RAISE EXCEPTION '000092 downgrade blocked: cron backup health usage or long alert code is permanent';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_cron_backup_health_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_cron_backup_health_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW
EXECUTE FUNCTION cron_backup_health_downgrade_admission();

COMMIT;
