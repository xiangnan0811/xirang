CREATE TABLE IF NOT EXISTS node_metric_samples (
    id SERIAL PRIMARY KEY,
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    cpu_pct DOUBLE PRECISION NOT NULL DEFAULT 0,
    mem_pct DOUBLE PRECISION NOT NULL DEFAULT 0,
    disk_pct DOUBLE PRECISION NOT NULL DEFAULT 0,
    load_1m DOUBLE PRECISION NOT NULL DEFAULT 0,
    sampled_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_node_metric_node_sampled ON node_metric_samples(node_id, sampled_at);
CREATE INDEX IF NOT EXISTS idx_node_metric_sampled_at ON node_metric_samples(sampled_at);
