DROP INDEX IF EXISTS idx_tasks_batch_id;
ALTER TABLE tasks DROP COLUMN batch_id;
