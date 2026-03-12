CREATE TABLE IF NOT EXISTS task_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL,
    trigger_type    VARCHAR(32) NOT NULL DEFAULT 'manual',
    status          VARCHAR(32) NOT NULL DEFAULT 'pending',
    started_at      DATETIME,
    finished_at     DATETIME,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    verify_status   VARCHAR(16) NOT NULL DEFAULT 'none',
    throughput_mbps REAL NOT NULL DEFAULT 0,
    last_error      TEXT,
    created_at      DATETIME,
    updated_at      DATETIME
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task_id ON task_runs(task_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_status ON task_runs(status);
CREATE INDEX IF NOT EXISTS idx_task_runs_task_status ON task_runs(task_id, status);
CREATE INDEX IF NOT EXISTS idx_task_runs_task_created ON task_runs(task_id, created_at DESC);
