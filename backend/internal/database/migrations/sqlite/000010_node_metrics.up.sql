CREATE TABLE IF NOT EXISTS node_metric_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    cpu_pct REAL NOT NULL DEFAULT 0,
    mem_pct REAL NOT NULL DEFAULT 0,
    disk_pct REAL NOT NULL DEFAULT 0,
    load_1m REAL NOT NULL DEFAULT 0,
    sampled_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_metric_node_sampled ON node_metric_samples(node_id, sampled_at);
CREATE INDEX IF NOT EXISTS idx_node_metric_sampled_at ON node_metric_samples(sampled_at);
