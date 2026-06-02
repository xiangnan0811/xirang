ALTER TABLE alerts ADD COLUMN slo_id INTEGER;

CREATE INDEX IF NOT EXISTS idx_alerts_slo_id ON alerts(slo_id) WHERE slo_id IS NOT NULL;

-- Backfill from error_code = 'XR-SLO-<id>'. The GLOB guard requires at least
-- one digit immediately after the prefix and matches only digits through end of
-- string (equivalent to Postgres ^XR-SLO-[0-9]+$). Rows with malformed tails
-- (e.g. 'XR-SLO-abc' or 'XR-SLO-5abc') are skipped and slo_id stays NULL.
-- GLOB doesn't support +, so we use two conditions: at least one leading digit,
-- and no non-digit characters anywhere after the prefix.
UPDATE alerts SET slo_id = CAST(SUBSTR(error_code, 8) AS INTEGER)
  WHERE error_code GLOB 'XR-SLO-[0-9]*'
    AND error_code NOT GLOB 'XR-SLO-[0-9]*[^0-9]*'
    AND slo_id IS NULL;
