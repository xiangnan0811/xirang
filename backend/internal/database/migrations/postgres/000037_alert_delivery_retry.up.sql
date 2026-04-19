ALTER TABLE alert_deliveries ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE alert_deliveries ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ NULL;
ALTER TABLE alert_deliveries ADD COLUMN IF NOT EXISTS last_error    TEXT NULL;

CREATE INDEX IF NOT EXISTS idx_alert_deliveries_retry
    ON alert_deliveries(status, next_retry_at)
    WHERE status = 'retrying';
