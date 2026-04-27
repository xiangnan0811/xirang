ALTER TABLE alerts ADD COLUMN slo_id INTEGER;

CREATE INDEX IF NOT EXISTS idx_alerts_slo_id ON alerts(slo_id) WHERE slo_id IS NOT NULL;

-- Backfill from error_code = 'XR-SLO-<id>'. The regex guard requires the
-- tail to be all digits; rows with malformed tails are skipped and slo_id
-- stays NULL.
UPDATE alerts SET slo_id = CAST(SUBSTRING(error_code FROM 8) AS INTEGER)
  WHERE error_code ~ '^XR-SLO-[0-9]+$' AND slo_id IS NULL;
