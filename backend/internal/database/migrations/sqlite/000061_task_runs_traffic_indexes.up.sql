-- Support overview traffic window queries on started/failed task runs.
-- Without these, SQLite plans full-table scans + temp sorts even with a row cap.
CREATE INDEX IF NOT EXISTS idx_task_runs_started_at ON task_runs(started_at);
CREATE INDEX IF NOT EXISTS idx_task_runs_status_finished_at ON task_runs(status, finished_at);
