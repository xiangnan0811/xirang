ALTER TABLE node_metric_samples ADD COLUMN latency_ms INTEGER;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_used REAL;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_total REAL;
ALTER TABLE node_metric_samples ADD COLUMN probe_ok INTEGER NOT NULL DEFAULT 1;
