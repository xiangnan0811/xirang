-- Remove the legacy `error` column on alert_deliveries.
-- Replaced by `last_error` since v0.12; both columns were written in lockstep
-- (see backend/internal/alerting/dispatcher.go), so dropping `error` does not
-- lose data. SQLite ≥3.35 supports ALTER TABLE ... DROP COLUMN natively.
ALTER TABLE alert_deliveries DROP COLUMN error;
