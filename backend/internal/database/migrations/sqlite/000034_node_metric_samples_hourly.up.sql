CREATE TABLE node_metric_samples_hourly (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    bucket_start DATETIME NOT NULL,
    cpu_pct_avg REAL,
    cpu_pct_max REAL,
    mem_pct_avg REAL,
    mem_pct_max REAL,
    disk_pct_avg REAL,
    disk_pct_max REAL,
    load1_avg REAL,
    load1_max REAL,
    latency_ms_avg REAL,
    latency_ms_max REAL,
    disk_gb_used_avg REAL,
    disk_gb_total REAL,
    probe_ok INTEGER NOT NULL DEFAULT 0,
    probe_fail INTEGER NOT NULL DEFAULT 0,
    sample_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    UNIQUE (node_id, bucket_start)
);
CREATE INDEX idx_nmsh_node_bucket ON node_metric_samples_hourly(node_id, bucket_start);
CREATE INDEX idx_nmsh_bucket ON node_metric_samples_hourly(bucket_start);
