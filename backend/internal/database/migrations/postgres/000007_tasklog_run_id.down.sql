DROP INDEX IF EXISTS idx_tasklog_run_cursor;
DROP INDEX IF EXISTS idx_task_logs_run_id;
ALTER TABLE task_logs DROP COLUMN task_run_id;
