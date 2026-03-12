ALTER TABLE alerts ADD COLUMN task_run_id INTEGER;
CREATE INDEX IF NOT EXISTS idx_alerts_task_run_id ON alerts(task_run_id);
