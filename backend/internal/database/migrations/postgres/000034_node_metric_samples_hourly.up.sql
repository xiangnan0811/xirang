CREATE TABLE IF NOT EXISTS node_metric_samples_hourly (
    id SERIAL PRIMARY KEY,
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    bucket_start TIMESTAMPTZ NOT NULL,
    cpu_pct_avg DOUBLE PRECISION,
    cpu_pct_max DOUBLE PRECISION,
    mem_pct_avg DOUBLE PRECISION,
    mem_pct_max DOUBLE PRECISION,
    disk_pct_avg DOUBLE PRECISION,
    disk_pct_max DOUBLE PRECISION,
    load1_avg DOUBLE PRECISION,
    load1_max DOUBLE PRECISION,
    latency_ms_avg DOUBLE PRECISION,
    latency_ms_max DOUBLE PRECISION,
    disk_gb_used_avg DOUBLE PRECISION,
    disk_gb_total DOUBLE PRECISION,
    probe_ok BIGINT NOT NULL DEFAULT 0,
    probe_fail BIGINT NOT NULL DEFAULT 0,
    sample_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (node_id, bucket_start)
);
CREATE INDEX IF NOT EXISTS idx_nmsh_node_bucket ON node_metric_samples_hourly(node_id, bucket_start);
CREATE INDEX IF NOT EXISTS idx_nmsh_bucket ON node_metric_samples_hourly(bucket_start);
