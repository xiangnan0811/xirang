DROP INDEX IF EXISTS idx_alert_deliveries_retry;
-- SQLite cannot DROP COLUMN on older versions; leave orphan columns on rollback
-- (project convention; matches prior migrations if they did the same).
