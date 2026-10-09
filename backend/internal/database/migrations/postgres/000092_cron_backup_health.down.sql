BEGIN;

-- The metadata admission trigger normally rejects a used downgrade or one
-- that would truncate a long alert identity before this body starts. Keep an
-- independent body guard for direct/manual execution.
CREATE TEMP TABLE cron_backup_health_000092_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO cron_backup_health_000092_down_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM cron_backup_health_usage)
                 OR EXISTS (SELECT 1 FROM alerts WHERE length(error_code) > 64)
            THEN 0 ELSE 1 END;
DROP TABLE cron_backup_health_000092_down_guard;
ALTER TABLE alerts ALTER COLUMN error_code TYPE VARCHAR(64);

DROP TRIGGER IF EXISTS trg_cron_backup_health_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS cron_backup_health_downgrade_admission();
DROP TABLE cron_backup_health;
DROP TABLE cron_backup_health_usage;

COMMIT;
