DROP INDEX IF EXISTS idx_alerts_task_run_id;
ALTER TABLE alerts DROP COLUMN task_run_id;
