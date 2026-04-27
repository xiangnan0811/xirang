-- Remove the legacy `error` column on alert_deliveries. Replaced by
-- `last_error` since v0.12; the dispatcher wrote both in lockstep, so this
-- drop does not lose recoverable data.
ALTER TABLE alert_deliveries DROP COLUMN error;
