CREATE TABLE node_metric_samples_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    cpu_pct REAL NOT NULL DEFAULT 0,
    mem_pct REAL NOT NULL DEFAULT 0,
    disk_pct REAL NOT NULL DEFAULT 0,
    load_1m REAL NOT NULL DEFAULT 0,
    sampled_at DATETIME NOT NULL,
    created_at DATETIME
);
INSERT INTO node_metric_samples_new
    SELECT id, node_id, cpu_pct, mem_pct, disk_pct, load_1m, sampled_at, created_at
    FROM node_metric_samples;
DROP TABLE node_metric_samples;
ALTER TABLE node_metric_samples_new RENAME TO node_metric_samples;
CREATE INDEX idx_node_metric_node_sampled ON node_metric_samples(node_id, sampled_at);
CREATE INDEX idx_node_metric_sampled_at ON node_metric_samples(sampled_at);
