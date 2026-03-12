CREATE TABLE IF NOT EXISTS task_runs (
    id              BIGSERIAL PRIMARY KEY,
    task_id         BIGINT NOT NULL,
    trigger_type    VARCHAR(32) NOT NULL DEFAULT 'manual',
    status          VARCHAR(32) NOT NULL DEFAULT 'pending',
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    verify_status   VARCHAR(16) NOT NULL DEFAULT 'none',
    throughput_mbps DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_error      TEXT,
    created_at      TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task_id ON task_runs(task_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_status ON task_runs(status);
CREATE INDEX IF NOT EXISTS idx_task_runs_task_status ON task_runs(task_id, status);
CREATE INDEX IF NOT EXISTS idx_task_runs_task_created ON task_runs(task_id, created_at DESC);
