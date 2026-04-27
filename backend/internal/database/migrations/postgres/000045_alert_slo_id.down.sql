DROP INDEX IF EXISTS idx_alerts_slo_id;
ALTER TABLE alerts DROP COLUMN slo_id;
