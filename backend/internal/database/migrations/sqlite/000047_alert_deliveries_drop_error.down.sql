-- Roll back: restore the legacy `error` column. Existing rows will have NULL;
-- application code wrote both `error` and `last_error` together so a fresh
-- restart re-populates `error` for new deliveries only.
ALTER TABLE alert_deliveries ADD COLUMN error TEXT;
