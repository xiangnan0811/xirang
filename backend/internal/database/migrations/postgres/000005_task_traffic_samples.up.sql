-- 000005_task_traffic_samples.up.sql (PostgreSQL)
-- 任务流量采样表

CREATE TABLE IF NOT EXISTS task_traffic_samples (
    id                BIGSERIAL PRIMARY KEY,
    task_id           BIGINT NOT NULL,
    node_id           BIGINT NOT NULL,
    run_started_at    TIMESTAMPTZ NOT NULL,
    sampled_at        TIMESTAMPTZ NOT NULL,
    throughput_mbps   DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_task_traffic_task_run_sample ON task_traffic_samples (task_id, run_started_at, sampled_at);
CREATE INDEX IF NOT EXISTS idx_task_traffic_node_sample ON task_traffic_samples (node_id, sampled_at);
CREATE INDEX IF NOT EXISTS idx_task_traffic_sampled_at ON task_traffic_samples (sampled_at);
