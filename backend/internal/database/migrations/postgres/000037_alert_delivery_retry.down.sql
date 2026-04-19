DROP INDEX IF EXISTS idx_alert_deliveries_retry;
ALTER TABLE alert_deliveries DROP COLUMN IF EXISTS last_error;
ALTER TABLE alert_deliveries DROP COLUMN IF EXISTS next_retry_at;
ALTER TABLE alert_deliveries DROP COLUMN IF EXISTS attempt_count;
