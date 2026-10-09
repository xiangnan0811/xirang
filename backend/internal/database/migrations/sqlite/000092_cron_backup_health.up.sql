-- 000092 stores one durable cron-backup fault cursor per state source.
-- The usage marker is written with the first enrollment and survives cursor
-- cleanup, so a downgrade cannot erase evidence that the schema was used.
CREATE TABLE cron_backup_health (
    source_key TEXT NOT NULL PRIMARY KEY
        CHECK (length(source_key) = 64 AND source_key NOT GLOB '*[^0-9a-f]*'),
    source_id TEXT NOT NULL DEFAULT ''
        CHECK (source_id = '' OR (length(source_id) = 32 AND source_id NOT GLOB '*[^0-9a-f]*')),
    highest_revision INTEGER NOT NULL DEFAULT 0
        CHECK (highest_revision >= 0),
    enrolled_at DATETIME NOT NULL,
    fault_active BOOLEAN NOT NULL DEFAULT 0,
    alert_id INTEGER,
    updated_at DATETIME NOT NULL,
    CONSTRAINT cron_backup_health_fault_alert_check
        CHECK (fault_active = 0 OR alert_id IS NOT NULL),
    FOREIGN KEY (alert_id) REFERENCES alerts(id) ON DELETE RESTRICT
);

CREATE TABLE cron_backup_health_usage (
    id INTEGER NOT NULL PRIMARY KEY
        CHECK (id = 1)
);

-- golang-migrate writes the target version before running a down migration.
-- Reject a used 000092 downgrade, or one that would truncate the exact
-- cron-backup alert identity, at that metadata boundary. SQLite ignores the
-- declared VARCHAR(64) width, so this is the same semantic admission check as
-- PostgreSQL without rebuilding the baseline alerts table.
DROP TRIGGER IF EXISTS trg_cron_backup_health_downgrade_admission;
CREATE TRIGGER trg_cron_backup_health_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 92
 AND (
    EXISTS (SELECT 1 FROM cron_backup_health_usage)
    OR EXISTS (SELECT 1 FROM alerts WHERE length(error_code) > 64)
 )
BEGIN
    SELECT RAISE(ABORT, '000092 downgrade blocked: cron backup health usage or long alert code is permanent');
END;

DROP TRIGGER IF EXISTS trg_cron_backup_health_downgrade_update_admission;
CREATE TRIGGER trg_cron_backup_health_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 92
 AND (
    EXISTS (SELECT 1 FROM cron_backup_health_usage)
    OR EXISTS (SELECT 1 FROM alerts WHERE length(error_code) > 64)
 )
BEGIN
    SELECT RAISE(ABORT, '000092 downgrade blocked: cron backup health usage or long alert code is permanent');
END;
