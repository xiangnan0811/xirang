CREATE TABLE IF NOT EXISTS task_traffic_samples (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id           INTEGER NOT NULL,
    node_id           INTEGER NOT NULL,
    run_started_at    DATETIME NOT NULL,
    sampled_at        DATETIME NOT NULL,
    throughput_mbps   REAL NOT NULL DEFAULT 0,
    created_at        DATETIME
);

CREATE INDEX IF NOT EXISTS idx_task_traffic_task_run_sample ON task_traffic_samples (task_id, run_started_at, sampled_at);
CREATE INDEX IF NOT EXISTS idx_task_traffic_node_sample ON task_traffic_samples (node_id, sampled_at);
CREATE INDEX IF NOT EXISTS idx_task_traffic_sampled_at ON task_traffic_samples (sampled_at);
