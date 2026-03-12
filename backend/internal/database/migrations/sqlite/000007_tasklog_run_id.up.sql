ALTER TABLE task_logs ADD COLUMN task_run_id INTEGER;
CREATE INDEX IF NOT EXISTS idx_task_logs_run_id ON task_logs(task_run_id);
CREATE INDEX IF NOT EXISTS idx_tasklog_run_cursor ON task_logs(task_run_id, id DESC);
