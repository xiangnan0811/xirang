ALTER TABLE alerts ADD COLUMN slo_id INTEGER;

CREATE INDEX IF NOT EXISTS idx_alerts_slo_id ON alerts(slo_id) WHERE slo_id IS NOT NULL;

-- Backfill from error_code = 'XR-SLO-<id>'. The GLOB guard requires at least
-- one digit immediately after the prefix; rows with malformed tails (e.g.
-- 'XR-SLO-abc') are skipped and slo_id stays NULL.
UPDATE alerts SET slo_id = CAST(SUBSTR(error_code, 8) AS INTEGER)
  WHERE error_code GLOB 'XR-SLO-[0-9]*' AND slo_id IS NULL;
