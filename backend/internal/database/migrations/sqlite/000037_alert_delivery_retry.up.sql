ALTER TABLE alert_deliveries ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE alert_deliveries ADD COLUMN next_retry_at DATETIME NULL;
ALTER TABLE alert_deliveries ADD COLUMN last_error    TEXT NULL;

CREATE INDEX IF NOT EXISTS idx_alert_deliveries_retry
    ON alert_deliveries(status, next_retry_at)
    WHERE status = 'retrying';
