ALTER TABLE node_metric_samples ADD COLUMN latency_ms INTEGER;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_used DOUBLE PRECISION;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_total DOUBLE PRECISION;
ALTER TABLE node_metric_samples ADD COLUMN probe_ok BOOLEAN NOT NULL DEFAULT TRUE;
